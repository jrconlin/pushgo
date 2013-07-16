#!/usr/bin/python

# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/.

import json
import time
import gevent
import sys

from ws4py.client.threadedclient import WebSocketClient

from loads.case import TestCase
from pushtest.utils import (get_uaid, get_rand, str_gen, send_http_put)


class WsClient(WebSocketClient):
    """ Load test for pushgo. Runs types of tests:
        - connect, hello, register, update, ack, close
        - connect, hello, register, update, close
        - connect, hello, register, update loop one channel, ack, close
        - connect, hello, register, update loop different channel, ack, close
        - connect, hello, register, update, ack, ping loop, close 

        Requires loads


    """

    endpoint = ""
    chan = ""
    uaid = ""
    version = 0
    count = 0
    sleep = 1
    client_type = ""

    max_sleep = 10
    max_updates = 5
    verbose = True

    client_types = {'conn_close':30, 
                    'conn_noack':5,
                    'one_chan':30, 
                    'multi_chan':30,
                    'ping_loop':5}  
                
    def opened(self):
        self.client_type = get_rand(self.client_types)
        # self.client_type = 'ping_loop'
        print '\n', self.client_type
        self.chan = str_gen(8)
        self.uaid = get_uaid()
        self.version = int(str_gen(8))
        self.hello()

    def closed(self, code, reason=None):
        print "Closed down", code, reason

    def hello(self):
        self.send('{"messageType":"hello", "channelIDs":[], "uaid":"%s"}'
                  % self.uaid)

    def reg(self):
        self.send(
            '{"messageType":"register", "channelID":"%s", "uaid":"%s"}' %
            (self.chan, self.uaid))    

    def unreg(self):
        self.send(
            '{"messageType":"unregister", "channelID":"%s"}' %
            self.uaid) 

    def put(self):
        send_http_put(self.endpoint, "version=%s" % self.version)

    def ack(self):
        self.send('{"messageType":"ack",  "updates": [{"channelID": "%s", "version": %s}]}'
                  % (self.chan, self.version))

    def ping(self):
        print 'ping'
        self.send('{}')

    def check_response(self, data):
        if "status" in data.keys():
            if data['status'] != 200:
                print 'ERROR status: ', data['status']
                self.close()
                # errors.append("status: %s != 200" % data['status'])

    def new_chan(self):
        self.chan = str_gen(8)
        self.version = int(str_gen(8))
        self.hello()

    def received_message(self, m):
        data = json.loads(m.data)
        self.check_response(data)

        if self.verbose:
            print '\nRECV', data

        if self.count > self.max_updates:
            self.close()

        if "messageType" in data:
            time.sleep(self.sleep)

            if data["messageType"] == "hello":
                self.reg()
            if data["messageType"] == "register":
                self.endpoint = data["pushEndpoint"]
                self.put()
            if data["messageType"] == "ping":
                self.ping()

            if data["messageType"] == "notification":
                if self.client_type != 'conn_noack':
                    self.ack()

                if self.client_type == 'conn_close' or self.client_type == 'conn_noack':
                    self.unreg()
                    self.close()
                if self.client_type == 'one_chan':
                    self.version += 1
                    self.put()
                elif self.client_type == 'multi_chan':   
                    self.unreg()             
                    self.new_chan()
                elif self.client_type == 'ping_loop':
                    self.ping()
            
            self.count += 1



class TestLoad(TestCase):

    def test_load(self):
        try:
            ws = WsClient("ws://localhost:8080")
            ws.connect()
            ws.run_forever()
        except KeyboardInterrupt:
            ws.close()
