===================================
Setting up a new Push Test Instance
===================================

These instructions will help you setup a new Pushgo test instance on AWS. A
single AWS t2.micro can run all the following without too much whimpering.

AWS Setup
=========

1) Login to the AWS console and go to EC2
2) Choose Instances under the Instances drop-down on the left sidebar
3) Click the big blue Launch Instance button
4) Select Community AMI's on the right side, then search for "coreos stable"
   in the search box and hit enter
5) Choose the highest CoreOS stable version you see (usually right under 367).
   Make sure to choose the one that has virtualization type of 'hvm'.
6) Choose instance t2.micro or better
7) Click "Review & Launch"
8) Click "Edit Security Groups", then Add Rule's to allow the following ports
   access from Anywhere:

   - 8090/tcp  Push Websocket
   - 8081/tcp  Push Endpoint
   - 8086/tcp  InfluxDB REST Endpoint
   - 8083/tcp  InfluxDB Admin UI
   - 8000/tcp  Grafana
   - 8080/tcp  Kibana
   - 9200/tcp  ElasticSearch
9) Click "Review & Launch"
10) Click "Launch", and choose your preferred SSH keypair
11) Note the public IP address of the instance created, you will need it below as ``PUBLIC_IP``

Machine Setup
=============

SSH into the AWS instance, then setup the environment var ``PUBLIC_IP`` to refer to the AWS instances public IP:

.. code-block:: bash

    $ PUBLIC_IP=AWS_PUBLIC_IP_HERE

Copy/Paste the following into your ssh session to the instance, these instructions will pull the appropriate containers and run them:

.. code-block:: text


    docker pull minimum2scp/es-kibana:latest
    docker pull kitcambridge/heka:dev
    docker pull kitcambridge/cadvisor:influxdb
    docker pull bbangert/pushgo:1.4rc5
    docker pull tutum/influxdb
    docker pull tutum/grafana

    INFLUX_CID=$(docker run -d -p 8083:8083 -p 8086:8086 --expose 8090 --expose 8099 -e PRE_CREATE_DB="pushgo" tutum/influxdb)
    INFLUX_IP=$(docker inspect $INFLUX_CID | grep IPAddress | cut -d '"' -f 4)

    docker run -d -p 8000:80 -e INFLUXDB_HOST=$PUBLIC_IP -e INFLUXDB_PORT=8086 -e INFLUXDB_NAME=pushgo -e INFLUXDB_USER=root \
        -e INFLUXDB_PASS=root -e INFLUXDB_IS_GRAFANADB=true -e HTTP_USER=admin -e HTTP_PASS=admin tutum/grafana


    ELASTIC_CID=$(docker run -d -p 8080:80 -p 9200:9200 minimum2scp/es-kibana)
    ELASTIC_IP=$(docker inspect $ELASTIC_CID | grep IPAddress | cut -d '"' -f 4)

    docker run -d --volume=/:/rootfs:ro --volume=/var/run:/var/run:rw --volume=/sys:/sys:ro --volume=/var/lib/docker/:/var/lib/docker:ro \
        kitcambridge/cadvisor:influxdb -storage_driver=influxdb -storage_driver_host=$INFLUX_IP:8086 -storage_driver_db=pushgo -storage_driver_buffer_duration=5.000000000s

    mkdir -p heka
    cat << EOF > heka/config.toml
    [hekad]
    maxprocs = 4
    base_dir = "/heka/data"
    share_dir = "/usr/share/heka"

    [ProtobufDecoder]

    [LogstreamerInput]
    log_directory = "/var/log"
    file_match = 'pushgo\.log'
    decoder = "ProtobufDecoder"
    parser_type = "message.proto"

    [StatsdInput]
    address = ":8125"

    [StatAccumInput]
    emit_in_payload = false
    emit_in_fields = true
    ticker_interval = 1

    [DashboardOutput]
    ticker_interval = 15

    [InfluxEncoder]
    type = "SandboxEncoder"
    filename = "lua_encoders/statmetric_influx.lua"

    [HttpOutput]
    message_matcher = "Type == 'heka.statmetric'"
    encoder = "InfluxEncoder"
    address = "http://$INFLUX_IP:8086/db/pushgo/series"
    method = "POST"
    username = "root"
    password = "root"

    [ESLogstashV0Encoder]
    es_index_from_timestamp = true

    [ElasticSearchOutput]
    message_matcher = "(Logger == 'pushgo-1.4') && (Type != 'metrics')"
    server = "http://$ELASTIC_IP:9200"
    flush_interval = 50
    encoder = "ESLogstashV0Encoder"
    EOF


    STATSD_CID=$(docker run -d --volume=/home/core/heka:/heka:rw --volume=/var/log:/var/log:ro -p 8125:8125/udp -p 4352:4352 kitcambridge/heka:dev hekad -config=/heka/config.toml)
    STATSD_IP=$(docker inspect $STATSD_CID | grep IPAddress | cut -d '"' -f 4)

    docker run -d --volume=/var/log:/var/log:rw \
        -e PUSHGO_METRICS_STATSD_SERVER=$STATSD_IP:8125 \
        -e PUSHGO_DEFAULT_RESOLVE_HOST=false \
        -e PUSHGO_DEFAULT_CURRENT_HOST=$PUBLIC_IP \
        -e PUSHGO_ROUTER_DEFAULT_HOST=$PUBLIC_IP \
        -e PUSHGO_DISCOVERY_TYPE=static \
        -e PUSHGO_DISCOVERY_CONTACTS=$PUBLIC_IP \
        -e PUSHGO_LOGGING_FILTER=7 \
        -p 8081:8081 -p 8090:8080 \
        bbangert/pushgo:1.4rc5

Verify Pushgo Connectivity
==========================

You should now be able to connect a Push test client to the PUBILC_IP:8090
endpoint and send notifications to channels registered.

Setup Grafana Dashboard
=======================

Go to http://PUBLIC_IP:8000/, and login with admin/admin as the grafana
container was set with.

Save the dashboard.json file from
https://gist.github.com/bbangert/394eda539d441687af49.

Open a dashboard in the Grafana UI, and select the dashboard.json that was
saved.

.. note::

    The dashboard graphs may be empty until data starts flowing from the
    pushgo server being used.

Setup Kibana
============

Kibana is already setup! Just go to
http://PUBLIC_IP:8080/index.html#/dashboard/file/logstash.json and watch the
data flow in.

Run a basic Push Test Client
============================

Want to make sure this actually works? Sure!

In your ssh session, pull the test client:

.. code-block:: bash

    $ docker pull bbangert/simpletest:dev

Now start it up:

.. code-block:: bash

    $ docker run -t -i bbangert/simpletest:dev $PUBLIC_IP 8090 1 ping $STATSD_IP:8125

You should now be able to see some data in the dashboards, and after 5 seconds
see some output on the ssh session that 1 client is connected.

You can hit Ctrl-C to stop it, and move on with such a happy happy life as
things work so wonderfully (Yea computers!).
