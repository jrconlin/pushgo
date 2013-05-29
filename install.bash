#! /bin/bash
set -e
if [ '$GOROOT' == '' ]; then
    echo "GOROOT not defined. Is go installed?"
    exit 1
fi
if [ '$GOBIN' == '' ]; then
    echo "GOBIN not defined. Is go installed?"
    exit 1
fi
export GOPATH=`pwd`
echo "Installing required go libraries..."
for req in `cat go_deps.lst`; do
    echo -n "   $req..."
    go get -v
    echo " done"
done
echo "Libraries installed"
echo "Please edit config.ini for local settings."
