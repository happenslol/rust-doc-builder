#!/bin/sh
echo "Creating folders..."
mkdir -p public/tags
mkdir -p amethyst/

echo "Updating tooling..."
cargo install-update -a

echo "Cloning amethyst..."
git clone https://github.com/amethyst/amethyst --branch master amethyst

cd amethyst
echo "Compiling master book..."
mdbook build book

echo "Compiling master docs..."
cargo doc --all --no-deps --quiet
cd ..

echo "Moving master to public dir..."
rm -rf public/book/master
mkdir -p public/book/master/
mv -f amethyst/book/book/* public/book/master/

rm -rf public/docs/master
mkdir -p public/docs/master
cp -rf amethyst/target/doc/ public/docs/master/

cd amethyst
LATEST_TAG=$(git describe --abbrev=0 --tags)
git checkout -q $LATEST_TAG

echo "Compiling stable book ($LATEST_TAG)..."
mdbook build book

echo "Compiling stable docs ($LATEST_TAG)..."
cargo doc --all --no-deps --quiet
cd ..

echo "Moving stable to public dir..."
rm -rf public/book/stable
mkdir -p public/book/stable
mv -f amethyst/book/book/* public/book/stable

rm -rf public/docs/stable
mkdir -p public/docs/stable/
cp -rf amethyst/target/doc/ public/docs/stable

cd amethyst
for tag in $(git tag)
do
    git checkout -q $tag

    echo "Compiling book $tag..."
    mdbook build book

    # This is pretty overkill
    # echo "Compiling docs $tag..."
    # cargo doc --all --no-deps --quiet

    cd ..

    rm -rf public/book/tags/$tag
    mkdir -p public/book/tags/$tag/
    mv -f amethyst/book/book/* public/book/tags/$tag/

    # rm -rf public/docs/tags/$tag
    # mkdir -p public/docs/tags/$tag/
    # cp -rf amethyst/target/doc/ public/docs/tags/$tag/

    cd amethyst
done
cd ..