#! /bin/bash
set -e
echo "Installing required go libraries..."
GOPATH="$(pwd):$GOPATH"
if [ ! -e $GOBIN/godep ]; then
    go get github.com/tools/godep
fi
godep restore
git submodule update --init
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
