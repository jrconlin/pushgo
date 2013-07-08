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
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})
        self.ws.close()

        # leading trailing spaces
        self.ws = websocket.create_connection(self.url)
        ret = self.msg(self.ws, {" messageType ": "hello",
                       "channelIDs": ["CASE_UAID"],
                       "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})
        self.ws.close()

        # Cap channelIds
        self.ws = websocket.create_connection(self.url)
        ret = self.msg(self.ws, {"messageType": "hello",
                       "ChannelIds": ["CASE_UAID"],
                       "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})
        self.ws.close()

        # all cap hello
        self.ws = websocket.create_connection(self.url)
        ret = self.msg(self.ws, {"messageType": "HELLO",
                       "channelIDs": ["CASE_UAID"],
                       "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 200, "messageType": "hello"})
        self.ws.close()

        # bad register case
        self.ws = websocket.create_connection(self.url)
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["CASE_UAID"],
                 "uaid": get_uaid("CASE_UAID")})
        ret = self.msg(self.ws, {"messageType": "registeR",
                       "channelIDs": ["CASE_UAID"],
                       "uaiD": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})

        # test ack
        self.msg(self.ws, {"messageType": "acK",
                 "channelIDs": ["CASE_UAID"],
                 "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})

        # test ping
        self.msg(self.ws, {"messageType": "PING",
                 "channelIDs": ["CASE_UAID"],
                 "uaid": get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})

    def test_empty_args(self):
        ret = self.msg(self.ws, {"messageType": "",
                       "channelIDs": ["CASE_UAID"],
                       "uaid": get_uaid("empty_uaid")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid Command'})

        tmp_uaid = get_uaid("empty_uaid")
        self.ws = websocket.create_connection(self.url)
        import pdb; pdb.set_trace();
        ret = self.msg(self.ws, {"messageType": "hello",
                       "channelIDs": [],
                       "uaid": tmp_uaid})
        self.compare_dict(ret, {"status": 200, "messageType": "hello"})

        ret = self.msg(self.ws, {"messageType": "hello",
                       "channelIDs": ["CASE_UAID"],
                       "uaid": ""})
        self.compare_dict(ret, {'status': 200,
                          "uaid": tmp_uaid,
                          "messageType": "hello"})

        #register
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["empty_chan"],
                 "uaid": get_uaid("empty_uaid")})

        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "CASE_UAID",
                       "uaid": ""})
        self.compare_dict(ret, {'status': 200, 'messageType': 'register'})
        self.validate_endpoint(ret['pushEndpoint'])

        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "",
                       "uaid": get_uaid("empty_uaid")})
        self.compare_dict(ret, {"status": 503,
                          "messageType": "register",
                          "error": "Service Unavailable"})

        # test ping
        # XXX Bug - ping after error isn't updated in response
        # self.msg(self.ws, {"messageType": "ping"})
        # self.compare_dict(ret, {'status': 200, 'messageType': 'ping'})

    def test_chan_limits(self):
        """ Test string limits for keys """
        self.msg(self.ws, {"messageType": "hello",
                 "channelIDs": ["chan_limits"],
                 "uaid": get_uaid("chan_limit_uaid")})

        ret = self.msg(self.ws, {"messageType": "register",
                       "channelID": "%s" % self.chan_150[: 101]})
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
                       "uaid": get_uaid("chan_limit_uaid")})
        self.compare_dict(ret, {"status": 401, "messageType": "hello"})

        # register 100 channels
        for chan in self.big_uuid:
            ret = self.msg(self.ws, {"messageType": "register",
                           "channelID": chan,
                           "uaid": get_uaid("chan_limit_uaid")})
            self.compare_dict(ret, {"status": 200, "messageType": "register"})
            self.validate_endpoint(ret['pushEndpoint'])

    def tearDown(self):
        self.ws.close()

if __name__ == '__main__':
    unittest.main(verbosity=2)
