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
	echo '8be06b5b95adbc0a7cb0f232e237b648caf783e1  libmemcached-1.0.18.tar.gz' | sha1sum -c || exit 1; \
	tar -xzvf libmemcached-1.0.18.tar.gz; \
	cd libmemcached-1.0.18; \
	./configure --prefix=/usr && make install; \
	cd; \
	rm -rf /usr/local/src; \
	apt-get remove -y -q bzr automake flex bison libtool cloog-ppl wget
# End RUN

# Setup our simplepush app
WORKDIR /gopath/src/app

# Copy the simplepush code into the container
ADD . /gopath/src/app/

# Build the simplepush server
RUN make
RUN make simplepush

# WebSocket listener port.
EXPOSE 8080
# HTTP update listener port.
EXPOSE 8081
# Profiling port.
EXPOSE 8082

# Internal routing port; should not be published.
EXPOSE 3000

ENV PUSHGO_METRICS_STATSD_HOST :8125

ENTRYPOINT ["./simplepush", "-config=config.docker.toml"]
