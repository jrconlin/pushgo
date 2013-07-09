#!/usr/bin/python

# import json
import unittest
import websocket

from pushtest.pushTestCase import PushTestCase
from pushtest.utils import get_uaid


class TestPushAPI(PushTestCase):
    """ General API tests """
    def setUp(self):
        if self.debug:
            websocket.enableTrace(False)
        self.ws = websocket.create_connection(self.url)

    def test_key_case(self):
        """ Test key case sensitivity """
        self.ws = websocket.create_connection(self.url)
        ret = self.msg(self.ws, {"messagetype": "hello",
                       "channelIDs": ["CASE_UAID"],
                       "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {"status": 401,
                          "error": "Invalid Command"})
        self.msg(self.ws, {"messageType": "purge"})
        self.ws.close()

        # leading trailing spaces
        self.ws = websocket.create_connection(self.url)
        ret = self.msg(self.ws, {" messageType ": "hello",
                       "channelIDs": ["CASE_UAID"],
                       "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})
        self.msg(self.ws, {"messageType": "purge"})
        self.ws.close()

        # Cap channelIds
        self.ws = websocket.create_connection(self.url)
        ret = self.msg(self.ws, {"messageType": "hello",
                       "ChannelIds": ["CASE_UAID"],
                       "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})
        self.msg(self.ws, {"messageType": "purge"})
        self.ws.close()

        # all cap hello
        self.ws = websocket.create_connection(self.url)
        ret = self.msg(self.ws, {"messageType": "HELLO",
                       "channelIDs": ["CASE_UAID"],
                       "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 200, "messageType": "hello"})
        self.msg(self.ws, {"messageType": "purge"})
        self.ws.close()

        # bad register case
        self.ws = websocket.create_connection(self.url)
        uaid = get_uaid("CASE_UAID")
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["CASE_UAID"],
                 "uaid": get_uaid("CASE_UAID")})
        ret = self.msg(self.ws, {"messageType": "registeR",
                       "channelIDs": ["CASE_UAID"],
                       "uaiD": uaid})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})

        # test ack
        self.msg(self.ws, {"messageType": "acK",
                 "channelIDs": ["CASE_UAID"],
                 "uaid": uaid})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})

        # test ping
        self.msg(self.ws, {"messageType": "PING",
                 "channelIDs": ["CASE_UAID"],
                 "uaid": uaid})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})
        self.msg(self.ws, {"messageType": "purge"})

    def test_empty_args(self):
        uaid = get_uaid("empty_uaid")
        ret = self.msg(self.ws, {"messageType": "",
                       "channelIDs": ["CASE_UAID"],
                       "uaid": uaid})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})
        self.msg(self.ws, {"messageType": "purge"})

        # Test that an empty UAID after "hello" returns the same UAID
        tmp_uaid = get_uaid("empty_uaid")
        self.ws = websocket.create_connection(self.url)
        ret = self.msg(self.ws, {"messageType": "hello",
                       "channelIDs": [],
                       "uaid": tmp_uaid})
        self.compare_dict(ret, {"status": 200, "messageType": "hello"})

        ret = self.msg(self.ws, {"messageType": "hello",
                       "channelIDs": [],
                       "uaid": ""})
        self.compare_dict(ret, {'status': 200,
                          "uaid": tmp_uaid,
                          "messageType": "hello"})
        self.msg(self.ws, {"messageType": "purge"})
        self.ws.close()
        self.ws = websocket.create_connection(self.url)

        #register (clearing the channel first in case it's already present)
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": [],
                 "uaid": uaid})

        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "EMPTY_ARG",
                       "uaid": ""})
        self.compare_dict(ret, {'status': 200, 'messageType': 'register'})
        self.validate_endpoint(ret['pushEndpoint'])

        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "",
                       "uaid": uaid})
        self.compare_dict(ret, {"status": 401,
                          "messageType": "register",
                          "error": "Invalid Command"})
        self.msg(self.ws, {"messageType": "purge"})
        # test ping
        # XXX Bug - ping after error isn't updated in response
        # self.msg(self.ws, {"messageType": "ping"})
        # self.compare_dict(ret, {'status': 200, 'messageType': 'ping'})

    def test_chan_limits(self):
        """ Test string limits for keys """
        uaid = get_uaid("chan_limit_uaid")
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["chan_limits"],
                 "uaid": uaid})
        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "%s" % self.chan_150[:101]})
        self.compare_dict(ret, {"status": 401,
                          "messageType": "register",
                          "error": "Invalid Command"})

        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "%s" % self.chan_150[:100]})
        self.compare_dict(ret, {"status": 200, "messageType": "register"})
        self.validate_endpoint(ret['pushEndpoint'])

        # hello 100 channels
        ret = self.msg(self.ws, {"messageType": "hello",
                       "channelIDs": self.big_uuid,
                       "uaid": uaid})
        self.compare_dict(ret, {"status": 401, "messageType": "hello"})

        # register 100 channels
        for chan in self.big_uuid:
            ret = self.msg(self.ws, {"messageType": "register",
                           "channelID": chan,
                           "uaid": uaid})
            self.compare_dict(ret, {"status": 200, "messageType": "register"})
            self.validate_endpoint(ret['pushEndpoint'])

    def tearDown(self):
        self.msg(self.ws, {"messageType": "purge"})
        self.ws.close()

if __name__ == '__main__':
    unittest.main(verbosity=2)
