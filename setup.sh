if (! -e root );
then
mkdir root
cp -r $GOROOT/ root
rm root/src
mkdir root/src
cp -r $GOROOT/src/* root/src
fi
set GOROOT=`pwd`/root
go get code.google.com/p/go.net/websocket
go get github.com/bradfitz/gomemcache/memcache
