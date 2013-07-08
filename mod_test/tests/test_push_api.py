#!/usr/bin/python

import json
import unittest
import websocket

from pushtest.pushTestCase import PushTestCase
from pushtest.utils import (get_uaid, str_gen, send_http_put)

## Note: The protocol notes that a re-registration with the same
#  channel number should return a 409. This can cause problems
#  for large servers, since it requires the server to maintain
#  states for each uaid+channel pair. These tests are commented
#  out until we can either detect the type of server we're running
#  against, or otherwise resolve the issue.
allowDupes = False


class TestPushAPI(PushTestCase):
    """ General API tests """
    def setUp(self):
        if self.debug:
            websocket.enableTrace(False)
        self.ws = websocket.create_connection(self.url)

    def test_hello_bad_types(self):
        """ Test handshake messageType with lots of data types """
        for dt in self.data_types:
            tmp_uaid = get_uaid("uaid")
            verify_json = {"messageType": "%s" % dt.lower(),
                           "status": 401,
                           "uaid": tmp_uaid}
            ret = self.msg(self.ws, {"messageType": '%s' % dt.lower(),
                           "channelIDs": [],
                           "uaid": tmp_uaid})
            if dt == 'HeLLO':
                verify_json["status"] = 200
            else:
                verify_json["error"] = "Invalid Command"
            self.compare_dict(ret, verify_json)

        # sending non self.strings to make sure it doesn't break server
        self.ws.send('{"messageType": 123}')
        self.assertEqual(self.ws.recv(), None)
        try:
            self.ws.send('{"messageType": null}')
        except Exception, (errno, msg):
            print 'Exception', errno, msg
            self.assertEqual(errno, 32)
            self.assertEqual(msg, 'Broken pipe')

    def test_hello_uaid_types(self):
        """ Test handshake uaids with lots of data types """
        unknown_uaid = str_gen(32)
        lstrings = list(self.strings)
        lstrings.append(unknown_uaid)
        for string in lstrings:
            valid_json = {"messageType": "hello"}
            ws = websocket.create_connection(self.url)
            msg = {"messageType": "hello",
                   "customKey": "custom value",
                   "channelIDs": [],
                   "uaid": "%s" % string}
            if string == unknown_uaid:
                # sending channelIDs with an unknown UAID should trigger
                # a client reset (return a different UAID)
                msg["channelIDs"] = ["1", "2"]
            ws.send(json.dumps(msg))
            ret = json.loads(ws.recv())
            if string == "valid_uaid":
                # Spec doesn't support sending hello's,
                # and empty returns last uaid
                valid_json["status"] = 200
                valid_json["uaid"] = "valid_uaid"
            elif string == "":
                valid_json["status"] = 200
                assert(len(ret["uaid"]) > 0)
            elif string == " fooey barrey ":
                valid_json["status"] = 401
            elif len(string) > 100:
                # 100 char limit for UAID and Channel
                valid_json["status"] = 401
                valid_json["error"] = "Invalid Command"
            elif string == unknown_uaid:
                assert(ret["uaid"] != unknown_uaid)
                continue
            self.compare_dict(ret, valid_json)

    def test_hello_invalid_keys(self):
        """ Test various json keys """
        for dt in self.data_types:
            invalid_ws = websocket.create_connection(self.url)
            invalid_ws.send(json.dumps({"%s" % dt: "hello"}))
            try:
                ret = json.loads(invalid_ws.recv())
            except Exception as e:
                print 'Exception - Unable to read socket: ', e

            if dt == 'messageType':
                self.compare_dict(ret, {"messageType": "hello",
                                        "status": 401,
                                        "error": "Invalid Command"})
            else:
                self.compare_dict(ret, {"status": 401,
                                  "error": "Invalid Command"})

            invalid_ws.close()

    def test_reg_noshake(self):
        """ Test registration without prior handshake """
        # no handshake invalid
        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "reg_noshake_chan_1",
                       "uaid": get_uaid("reg_noshake_uaid_1")})
        self.compare_dict(ret, {"messageType": "register",
                          "status": 401,
                          "error": "Invalid Command"})

        # valid
        ret = self.msg(self.ws, {"messageType": "hello",
                       "channelIDs": ["reg_noshake_chan_1"],
                       "uaid": get_uaid("reg_noshake_uaid_1")})
        if allowDupes:
            ret = self.msg(self.ws, {"messageType": "register",
                           "channelID": get_uaid("reg_noshake_chan_1")})
            self.compare_dict(ret, {"messageType": "register",
                              "status": 200})
            self.validate_endpoint(ret['pushEndpoint'])
        #clean-up
        self.msg(self.ws, {"messageType": "unregister",
                           "channelID": "reg_noshake_chan_1"})

    def test_reg_duplicate(self):
        """ Test registration with duplicate channel name """
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": [get_uaid("reg_noshake_chan_1")],
                 "uaid": get_uaid("reg_noshake_uaid_1")})
        if allowDupes:
            ret = self.msg(self.ws, {"messageType": "register",
                           "channelID": "dupe_handshake"})
            self.compare_dict(ret, {"messageType": "register",
                              "status": 200})
            # duplicate handshake
            ret = self.msg(self.ws, {"messageType": "register",
                           "channelID": "dupe_handshake"})
            self.compare_dict(ret, {"messageType": "register",
                              "status": 200})

        # passing in list to channelID
        ret = self.msg(self.ws, {"messageType": "register",
                       "channelIDs": ["chan_list"]})
        self.compare_dict(ret, {"messageType": "register",
                          "status": 401,
                          "error": "Invalid Command"})
        self.msg(self.ws, {"messageType": "unregister",
                           "channelID": "dupe_handshake"})

    def test_reg_plural(self):
        """ Test registration with a lot of channels and uaids """
        # XXX bug uaid can get overloaded with channels,
        # adding epoch to unique-ify it.

        if allowDupes:
            self.msg(self.ws, {"messageType": "hello",
                     "channelIDs": ["reg_plural_chan"],
                     "uaid": get_uaid("reg_plural")})
            ret = self.msg(self.ws, {"messageType": "register",
                           "channelID": "reg_plural_chan",
                           "uaid": get_uaid("reg_plural")})

            self.compare_dict(ret, {"messageType": "register",
                              "status": 200})

            # valid with same channelID
            ret = self.msg(self.ws, {"messageType": "register",
                           "channelID": "reg_plural_chan"})
            self.compare_dict(ret, {"messageType": "register",
                              "status": 200})

        # loop through different channelID values
        for dt in self.data_types:
            ret = self.msg(self.ws, {"messageType": "register",
                           "channelID": "%s" % dt,
                           "uaid": get_uaid("diff_uaid")})
            if 'error' in ret:
                # lots of errors here, lots of gross logic to
                # validate them here
                continue
            self.compare_dict(ret, {"messageType": "register",
                              "status": 200})

    def test_unreg(self):
        """ Test unregister """
        # unreg non existent
        ret = self.msg(self.ws, {"messageType": "unregister"})
        self.compare_dict(ret, {"messageType": "unregister",
                          "status": 401,
                          "error": "Invalid Command"})

        # unreg a non existent channel
        ret = self.msg(self.ws, {"messageType": "unregister",
                       "channelID": "unreg_chan"})
        self.compare_dict(ret, {"messageType": "unregister",
                          "status": 401,
                          "error": "Invalid Command"})

        # setup
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["unreg_chan"],
                 "uaid": get_uaid("unreg_uaid")})
        self.msg(self.ws, {"messageType": "register",
                 "channelID": "unreg_chan"})
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["unreg_chan"],
                 "uaid": get_uaid("unreg_uaid")})

        # unreg
        ret = self.msg(self.ws, {"messageType": "unregister",
                       "channelID": "unreg_chan"})
        self.compare_dict(ret, {"messageType": "unregister",
                          "status": 200})

        # check if channel exists
        ret = self.msg(self.ws, {"messageType": "unregister",
                       "channelID": "unreg_chan"})
        # XXX No-op on server results in this behavior
        self.compare_dict(ret, {"messageType": "unregister",
                          "status": 200})

    def test_ping(self):
        # Ping responses can contain any data.
        # The reference server returns the minimal data set "{}"
        # happy
        ret = self.msg(self.ws, {})
        if ret != {}:
            self.compare_dict(ret, {"messageType": "ping",
                                    "status": 200})

        # happy
        ret = self.msg(self.ws, {'messageType': 'ping'})
        if ret != {}:
            self.compare_dict(ret, {"messageType": "ping",
                              "status": 200})

        # extra args
        ret = self.msg(self.ws, {'messageType': 'ping',
                       'channelIDs': ['ping_chan'],
                       'uaid': get_uaid('ping_uaid'),
                       'nada': ''})
        if ret != {}:
            self.compare_dict(ret, {"messageType": "ping",
                              "status": 200})

        # do a register between pings
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["ping_chan_1"],
                 "uaid": get_uaid("ping_uaid")})
        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "ping_chan_1a"})
        self.compare_dict(ret, {"status": 200, "messageType": "register"})

        # send and ack too
        # XXX ack can hang socket
        # ret = self.msg(self.ws, {"messageType": "ack",
        #                 "updates": [{ "channelID": get_uaid("ping_chan_1"),
        #                 "version": 123 }]})
        # self.compare_dict(ret, {"status": 200, "messageType": "ack"})

        # empty braces is a valid ping
        ret = self.msg(self.ws, {})
        if ret != {}:
            self.compare_dict(ret, {"messageType": "ping",
                              "status": 200})

        for ping in range(100):
            ret = self.msg(self.ws, {'messageType': 'ping'})
            if ret != {}:
                self.compare_dict(ret, {"messageType": "ping",
                                  "status": 200})
        #cleanup
        self.msg(self.ws, {"messageType": "unregister",
                 "channelID": "ping_chan_1"})
        self.msg(self.ws, {"messageType": "unregister",
                 "channelID": "ping_chan_1a"})

    def test_ack(self):
        """ Test ack """
        # no hello
        ret = self.msg(self.ws, {"messageType": "ack",
                       "updates": [{"channelID": "ack_chan_1",
                                    "version": 23}]})
        self.compare_dict(ret, {"error": "Invalid Command",
                          "status": 401, "messageType": "ack"})
        self.assertEqual(ret["updates"][0]["channelID"], "ack_chan_1")
        self.assertEqual(ret["updates"][0]["version"], 23)

        # happy path
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["ack_chan_1"],
                 "uaid": get_uaid("ack_uaid")})
        reg = self.msg(self.ws, {"messageType": "register",
                       "channelID": "ack_chan_1"})

        # send an http PUT request to the endpoint
        send_http_put(reg['pushEndpoint'])

        # this blocks the socket on read
        # print 'RECV', self.ws.recv()
        # hanging socket against AWS
        ret = self.msg(self.ws, {"messageType": "ack",
                       "updates": [{"channelID": "ack_chan_1",
                                    "version": 23}]})
        self.compare_dict(ret, {"messageType": "notification",
                          "expired": None})
        self.assertEqual(ret["updates"][0]["channelID"], "ack_chan_1")

    def tearDown(self):
        self.ws.close()

if __name__ == '__main__':
    unittest.main(verbosity=2)
