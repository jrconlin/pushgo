#!/usr/bin/python

import json, unittest, websocket

from pushtest.pushTestCase import PushTestCase
from pushtest.utils import *

class TestPushAPI(PushTestCase):
    """ General API tests """
    def setUp(self):
        if self.debug:
            websocket.enableTrace(False)
        self.ws = websocket.create_connection(self.url)

    def test_key_case(self):
        """ Test key case sensitivity """
        ret = self.msg(self.ws, {"messagetype": "hello", "channelIDs": ["CASE_UAID"], "uaid":get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid command'})       
        
        # leading trailing spaces
        ret = self.msg(self.ws, {" messageType ": "hello", "channelIDs": ["CASE_UAID"], "uaid":get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid command'})  
        # Cap channelIds
        ret = self.msg(self.ws, {"messageType": "hello", "ChannelIds": ["CASE_UAID"], "uaid":get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid command'}) 
        # all cap hello
        ret = self.msg(self.ws, {"messageType": "HELLO", "channelIDs": ["CASE_UAID"], "uaiD":get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 200, "messageType": "HELLO"}) 

        # bad register case
        self.msg(self.ws, {"messageType": "hello", "channelIDs": ["CASE_UAID"], "uaid":get_uaid("CASE_UAID")})
        ret = self.msg(self.ws, {"messageType": "registeR", "channelIDs": ["CASE_UAID"], "uaiD":get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid command'}) 

        # test ack
        self.msg(self.ws, {"messageType": "acK", "channelIDs": ["CASE_UAID"], "uaid":get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid command'}) 

        # test ping
        self.msg(self.ws, {"messageType": "PING", "channelIDs": ["CASE_UAID"], "uaid":get_uaid("CASE_UAID")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid command'}) 

    def test_empty_args(self):
        ret = self.msg(self.ws, {"messageType": "", "channelIDs": ["CASE_UAID"], "uaid":get_uaid("empty_uaid")})
        self.compare_dict(ret, {'status': 401, 'error': 'Invalid command'})  
        
        tmp_uaid = get_uaid("empty_uaid")
        ret = self.msg(self.ws, {"messageType": "hello", "channelIDs": [], "uaid":tmp_uaid})
        self.compare_dict(ret, {'status': 200, 'messageType':'hello'})  
        
        ret = self.msg(self.ws, {"messageType": "hello", "channelIDs": ["CASE_UAID"], "uaid":""})
        self.compare_dict(ret, {'status': 200, "uaid":tmp_uaid , 'messageType':'hello'})  

        #register
        self.msg(self.ws, {"messageType": "hello", "channelIDs": ['empty_chan'], "uaid":get_uaid("empty_uaid")})

        ret = self.msg(self.ws, {"messageType": "register", "channelID": "CASE_UAID", "uaid":""})
        self.compare_dict(ret, {'status': 200, 'messageType':'register'})  
        self.validate_endpoint(ret['pushEndpoint'])

        ret = self.msg(self.ws, {"messageType": "register", "channelID": "", "uaid":get_uaid("empty_uaid")})
        self.compare_dict(ret, {'status': 503, 'messageType':'register','error': 'Service Unavailable'}) 
        
        # test ping
        # XXX Bug - ping after error isn't updated in response
        # self.msg(self.ws, {"messageType": "ping"})
        # self.compare_dict(ret, {'status': 200, 'messageType': 'ping'}) 

    def test_chan_limits(self):
        """ Test string limits for keys """
        self.msg(self.ws, {"messageType": "hello", 
                  "channelIDs": ["chan_limits"], "uaid":get_uaid("chan_limit_uaid")})

        ret = self.msg(self.ws, {"messageType": "register", "channelID":"%s" % self.chan_150[:101]})
        self.compare_dict(ret, {"status": 401, "messageType": "register", 
                               "error": "Invalid command"})

        ret = self.msg(self.ws, {"messageType": "register", "channelID":"%s" % self.chan_150[:100]})
        self.compare_dict(ret, {"status": 200, "messageType": "register"})
        self.validate_endpoint(ret['pushEndpoint'])

        # hello 100 channels
        ret = self.msg(self.ws, {"messageType": "hello", "channelIDs": self.big_uuid , "uaid":get_uaid("chan_limit_uaid")})
        self.compare_dict(ret, {"status": 401, "messageType": "hello"})

        # register 100 channels
        for chan in self.big_uuid :
            ret = self.msg(self.ws, {"messageType": "register", "channelID": chan, "uaid":get_uaid("chan_limit_uaid")})
            self.compare_dict(ret, {"status": 200, "messageType": "register"})
            self.validate_endpoint(ret['pushEndpoint'])

    def tearDown(self):
        self.ws.close()

if __name__ == '__main__':
    unittest.main(verbosity=2)
