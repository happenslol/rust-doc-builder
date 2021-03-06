package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/hostrouter"
	"github.com/go-chi/render"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudfront"
)

var secret string
var scriptPath string
var notFoundPage string

const semverRegex = "^v(0|[0-9]+).(0|[0-9]+)(.(0|[0-9]+))?$"

func main() {
	f, err := ioutil.ReadFile("./404.html")
	if err != nil {
		log.Fatalf("couldn't read 404 page: %s\n", err.Error())
	}

	notFoundPage = string(f)

	secret = getEnvOr("SECRET", "123")
	port := getEnvOr("PORT", "3000")
	scriptPath = getEnvOr("SCRIPT", "./run.sh")

	log.Printf("using script %s\n", scriptPath)

	catchAll := chi.NewRouter()
	catchAll.Get("/health", handleHealth)
	catchAll.NotFound(handleNotFound)

	trigger := chi.NewRouter()
	trigger.Post("/", handleTrigger)
	trigger.NotFound(handleNotFound)

	r := chi.NewRouter()
	hr := hostrouter.New()

	docsURL := getEnvOr("DOCS_URL", "docs.amethyst.rs")
	bookURL := getEnvOr("BOOK_URL", "book.amethyst.rs")
	triggerURL := getEnvOr("TRIGGER_URL", "hook.amethyst.rs")

	docsBaseURL := getEnvOr("DOCS_BASE_URL", "docs.amethyst.rs")
	bookBaseURL := getEnvOr("BOOK_BASE_URL", "book.amethyst.rs")

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	hr.Map(docsURL, serveSubDirectory("docs", "/amethyst/", docsBaseURL))
	hr.Map(bookURL, serveSubDirectory("book", "/", bookBaseURL))
	hr.Map(triggerURL, trigger)
	hr.Map("*", catchAll)

	r.Mount("/", hr)
	r.NotFound(handleNotFound)

	log.Printf("serving on port %s\n", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), r))
}

func getEnvOr(s, def string) string {
	result := def
	if val, ok := os.LookupEnv(s); ok {
		result = val
	}

	return result
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	render.HTML(w, r, notFoundPage)
}

func serveSubDirectory(subdir, root, baseURL string) chi.Router {
	stablePath := fmt.Sprintf("./public/%s/stable", subdir)
	masterPath := fmt.Sprintf("./public/%s/master", subdir)
	tagsPath := fmt.Sprintf("./public/%s/tags", subdir)

	mustMkDir(stablePath)
	mustMkDir(masterPath)
	mustMkDir(tagsPath)

	stable := http.Dir(stablePath)
	master := http.Dir(masterPath)
	tags := http.Dir(tagsPath)

	stablePrefix := "/stable"
	masterPrefix := "/master"

	stableFs := http.StripPrefix(stablePrefix, http.FileServer(stable))
	masterFs := http.StripPrefix(masterPrefix, http.FileServer(master))
	tagsFs := http.FileServer(tags)

	tagsURL := fmt.Sprintf("/{tag:%s}/*", semverRegex)

	r := chi.NewRouter()
	stableRoot := fmt.Sprintf("//%s/stable%s", baseURL, root)
	r.Get("/stable", http.RedirectHandler(stableRoot, 301).ServeHTTP)

	r.
		With(makeHTMLMiddleware(stablePrefix, stablePath)).
		Get("/stable/*", stableFs.ServeHTTP)

	masterRoot := fmt.Sprintf("//%s/master%s", baseURL, root)
	r.Get("/master", http.RedirectHandler(masterRoot, 301).ServeHTTP)

	r.
		With(makeHTMLMiddleware(masterPrefix, masterPath)).
		Get("/master/*", masterFs.ServeHTTP)

	r.
		With(makeHTMLMiddleware("", tagsPath)).
		Get(tagsURL, tagsFs.ServeHTTP)

	r.Get("/", http.RedirectHandler(stableRoot, 301).ServeHTTP)
	r.NotFound(handleNotFound)

	return r
}

func makeHTMLMiddleware(prefix, root string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// first, check if there's a slash at the end
			if strings.HasSuffix(r.URL.Path, "/") {
				next.ServeHTTP(w, r)
				return
			}

			// or if we already have an extension
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) > 0 {
				last := parts[len(parts)-1]
				if strings.LastIndex(last, ".") > 0 {
					next.ServeHTTP(w, r)
					return
				}
			}

			endingsToTry := []string{".html", "htm"}
			stripped := strings.TrimPrefix(r.URL.Path, prefix)

			for _, ext := range endingsToTry {
				path := root + stripped + ext
				stat, err := os.Stat(path)
				if err != nil {
					continue
				}

				if stat.IsDir() {
					continue
				}

				// if we drop through to here, the file exists
				// and we should adjust the path so that it will
				// be served!
				r.URL.Path = r.URL.Path + ext
				break
			}

			next.ServeHTTP(w, r)
		})
	}
}

func mustMkDir(p string) {
	if err := os.MkdirAll(p, 0755); err != nil {
		log.Fatalf("could not create dir %s: %s\n", p, err.Error())
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte("up"))
}

func handleTrigger(w http.ResponseWriter, r *http.Request) {
	eventType := r.Header.Get("X-GitHub-Event")
	if eventType != "push" {
		log.Printf("ignoring non-push event: %s\n", eventType)
		w.WriteHeader(204)
		return
	}

	if r.Body == nil {
		http.Error(w, "empty body", 400)
		return
	}

	digest := r.Header.Get("X-Hub-Signature")
	if digest == "" {
		http.Error(w, "empty secret header", 403)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	key := []byte(secret)
	h := hmac.New(sha1.New, key)
	h.Write(body)
	hex := hex.EncodeToString(h.Sum(nil))
	calc := fmt.Sprintf("sha1=%s", hex)

	if calc != digest {
		log.Printf("sha1 didn't match: %s\n", calc)
		http.Error(w, "invalid secret", 403)
		return
	}

	r.Body.Close()
	r.Body = ioutil.NopCloser(bytes.NewBuffer(body))

	var b map[string]interface{}
	err = json.NewDecoder(r.Body).Decode(&b)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	ref, ok := b["ref"].(string)
	if !ok {
		http.Error(w, "no ref present", 400)
		return
	}

	if ref != "refs/heads/master" {
		log.Printf("ignoring push to ref: %s\n", ref)
		w.WriteHeader(204)
		return
	}

	log.Printf("executing script: %s\n", scriptPath)
	go runScript()

	w.WriteHeader(204)
}

func runScript() {
	cmd := exec.Command("/bin/sh", scriptPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("couldn't get stdout: %s\n", err.Error())
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("couldn't get stderr: %s\n", err.Error())
		return
	}

	err = cmd.Start()
	if err != nil {
		log.Printf("couldn't start cmd: %s\n", err.Error())
		return
	}

	stdoutScanner := bufio.NewScanner(stdout)
	stderrScanner := bufio.NewScanner(stderr)

	go print(stdoutScanner, "--->")
	go print(stderrScanner, "!!->")

	log.Print("waiting for output\n")
	err = cmd.Wait()
	if err != nil {
		log.Printf("err waiting for cmd: %s\n", err)
	}

	log.Printf("creating cloudfront invalidation\n")

	_, awsIDPresent := os.LookupEnv("AWS_ACCESS_KEY_ID")
	_, awsKeyPresent := os.LookupEnv("AWS_SECRET_ACCESS_KEY")

	if awsIDPresent && awsKeyPresent {
		svc := cloudfront.New(session.New())
		bookID := getEnvOr("BOOK_CDN_DIST_ID", "")
		docsID := getEnvOr("DOCS_CDN_DIST_ID", "")

		if bookID != "" {
			if err := invalidate(svc, bookID); err != nil {
				log.Printf("error invalidating book cdn: %v\n", err)
			}
		}

		if docsID != "" {
			if err := invalidate(svc, docsID); err != nil {
				log.Printf("error invalidating docs cdn: %v\n", err)
			}
		}
	}

	log.Printf("command ran\n")
}

func invalidate(svc *cloudfront.CloudFront, id string) error {
	callerRef := strconv.FormatInt(time.Now().Unix(), 10)

	all := "/*"
	items := []*string{&all}
	paths := cloudfront.Paths{}
	paths.SetItems(items)
	paths.SetQuantity(1)

	batch := cloudfront.InvalidationBatch{}
	batch.SetCallerReference(callerRef)
	batch.SetPaths(&paths)

	input := &cloudfront.CreateInvalidationInput{}
	input.SetDistributionId(id)
	input.SetInvalidationBatch(&batch)

	_, err := svc.CreateInvalidation(input)
	return err
}

func print(s *bufio.Scanner, pre string) {
	s.Split(bufio.ScanLines)
	for s.Scan() {
		m := s.Text()
		log.Printf("%s %s\n", pre, m)
	}
}
