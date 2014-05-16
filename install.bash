#! /bin/bash
set -e
echo "Installing required go libraries..."
git submodule update --init
for req in `grep -v "^#" go_deps.lst`; do
    go get -d -v $req
done
echo "Libraries installed"

if [ ! -e config.ini ]; then
    echo "Copying sample ini file to config.ini"
    cp config.sample.ini config.ini
fi
if [ ! -z "$HOST" ]; then
    echo "Setting local shard host name"
    echo "shard.current_host = $HOST:8080" >> config.ini
fi
echo "Please edit config.ini for local settings."
