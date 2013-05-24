#! /bin/bash
set -e
if [ '$GOROOT' == '' ]; then
    echo "GOROOT not defined. Is go installed?"

fi
if [ '$GOBIN' == '' ]; then
    echo "GOBIN not defined. Is go installed?"
    return
fi
export GOPATH=`pwd`
echo "Installing required go libraries..."
for req in `cat go_deps.lst`; do
    go get $req
done
echo "Libraries installed"
echo "Please edit config.ini for local settings."
