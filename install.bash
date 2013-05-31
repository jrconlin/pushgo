#! /bin/bash
set -e
for env in 'GOROOT' 'GOBIN' ; do
    if [ -z '${$env}' ]; then
        echo "$env not defined. Is go installed?"
        exit 1
    fi
done
export GOPATH=`pwd`
echo "Installing required go libraries..."
for req in `cat go_deps.lst`; do
    echo -n "   $req..."
    go get -v
    echo " done"
done
echo "Libraries installed"
echo "Please edit config.ini for local settings."
