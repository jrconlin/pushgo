# Mozilla SimplePush Server

# VERSION    0.1

# Extend google golang image
FROM google/golang

MAINTAINER Ben Bangert <bbangert@mozilla.com>

# Add libmemcached, the one it comes with isn't new enough, so we grab what
# we need to build it, build it, then remove all the stuff we made to build it
# so that this docker layer only contains the libmemcached addition
RUN \
	apt-get install --no-install-recommends -y -q bzr automake flex bison libtool cloog-ppl wget; \
	cd /usr/local/src; \
	wget https://launchpad.net/libmemcached/1.0/1.0.18/+download/libmemcached-1.0.18.tar.gz; \
	tar -xzvf libmemcached-1.0.18.tar.gz; \
	cd libmemcached-1.0.18; \
	./configure --prefix=/usr && make install; \
	rm -rf /usr/local/src; \
	apt-get remove -y -q bzr automake flex bison libtool cloog-ppl wget
# End RUN

# Setup our simplepush app
WORKDIR /gopath/src/app

# Copy the simplepush code into the container
ADD . /gopath/src/app/

# Setup the proper env so that we can install and build the simplepush server
ENV GOPATH /gopath/src/app/
RUN ./install.bash
RUN go build simplepush.go

ENTRYPOINT ["./simplepush"]