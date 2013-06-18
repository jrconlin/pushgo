#!/usr/bin/python

import ConfigParser, json, unittest, websocket
from utils import *

# config
config = ConfigParser.ConfigParser()
config.read('config.ini')
url = config.get('server', 'url')
debug = str2bool(config.get('debug', 'trace'))

# data types
uuid = ['f7067d44-5893-407c-8d4c-fc8f7ed97041',
        '1c57e340-df59-44648105-b91f1a39608b', 
        '0cb8a613-8e2b-4b47-b370-51098daa8401',
        '14a84c48-2b8c-4669-8976--541368ccf4d3']
big_uuid = uuid * 100

chan_150 = str_gen(150)
strings = ['', 'valid_uaid', ' fooey barrey ', '!@#$%^&*()-+', '0', '1', '-66000', uuid[0], 
           'True', 'False', 'true', 'false' , '\"foo bar\"',str_gen(64000)]
data_types = ['messageType', 'HeLLO', '!@#$%^&*()-+', '0', '1', '-66000', '',
              1, 0, -1, True, False, None, ' fooey barrey ', str_gen(64000), chr(0), '\x01\x00\x12\x59']

class TestPushAPI(unittest.TestCase):
    """ General API tests """
    def setUp(self):
        if debug:
            websocket.enableTrace(False)
        self.ws = websocket.create_connection(url)

    def _log(self, prefix, msg):
        if debug:
            log(prefix, msg)

    def _msg(self, msg, debug=0):
        """ Util that sends and returns dict"""
        self._log("SEND:", msg)
        try:
            self.ws.send(json.dumps(msg))
        except Exception as e:
            print 'Unable to send data', e
            return None
        try:
            ret = self.ws.recv()
            return json.loads(ret)
        except Exception, e:
            print 'Unable to parse json:', e
            raise AssertionError, e

    def _compare_dict(self, ret_data, exp_data):
        """ compares two dictionaries and raises assert with info """
        self._log("RESPONSE GOT:", ret_data)
        self._log("RESPONSE EXPECTED:", exp_data)
   
        diff = compare_dict(ret_data, exp_data)
        if diff["errors"]:
            raise AssertionError, diff["errors"]

    def _validate_endpoint(self, endpoint):
        """ validate endpoint is in proper url format """
        self.assertRegexpMatches(endpoint, r'(http|https)://.*/update/.*')

    def test_key_case(self):
        """ Test key case sensitivity """
        ret = self._msg({"messagetype": "hello", "channelIDs": [add_epoch("CASE_CHAN")], "uaid":"CASE_UAID"})
        self._compare_dict(ret, {'status': 401, 'error': 'Unknown command'})       
        
        # leading trailing spaces
        ret = self._msg({" messageType ": "hello", "channelIDs": [add_epoch("CASE_CHAN")], "uaid":"CASE_UAID"})
        self._compare_dict(ret, {'status': 401, 'error': 'Unknown command'})  

        ret = self._msg({"messageType": "hello", "ChannelIds": [add_epoch("CASE_CHAN")], "uaid":"CASE_UAID"})
        self._compare_dict(ret, {'status': 401, 'error': 'Missing required fields for command'}) 

        ret = self._msg({"messageType": "hello", "channelIDs": [add_epoch("CASE_CHAN")], "uaiD":"CASE_UAID"})
        self._compare_dict(ret, {'status': 401, 'error': 'Missing required fields for command'}) 

        # bad register case
        self._msg({"messageType": "hello", "channelIDs": [add_epoch("CASE_CHAN")], "uaid":"CASE_UAID"})
        ret = self._msg({"messageType": "registeR", "channelIDs": [add_epoch("CASE_CHAN")], "uaiD":"CASE_UAID"})
        self._compare_dict(ret, {'status': 401, 'error': 'Missing required fields for command'}) 

        # test ack
        self._msg({"messageType": "acK", "channelIDs": [add_epoch("CASE_CHAN")], "uaid":"CASE_UAID"})
        self._compare_dict(ret, {'status': 401, 'error': 'Missing required fields for command'}) 

        # test ping
        self._msg({"messageType": "PING", "channelIDs": [add_epoch("CASE_CHAN")], "uaid":"CASE_UAID"})
        self._compare_dict(ret, {'status': 401, 'error': 'Missing required fields for command'}) 

    def test_empty_args(self):
        ret = self._msg({"messageType": "", "channelIDs": [add_epoch("CASE_CHAN")], "uaid":"CASE_UAID"})
        self._compare_dict(ret, {'status': 401, 'error': 'Unknown command'})  
        
        ret = self._msg({"messageType": "hello", "channelIDs": [], "uaid":"CASE_UAID"})
        self._compare_dict(ret, {'status': 200, 'messageType':'hello'})  
        
        ret = self._msg({"messageType": "hello", "channelIDs": [add_epoch("CASE_CHAN")], "uaid":""})
        self._compare_dict(ret, {'status': 200, "uaid":"CASE_UAID" , 'messageType':'hello'})  

        #register
        self._msg({"messageType": "hello", "channelIDs": [add_epoch('empty_chan')], "uaid":"CASE_UAID"})

        ret = self._msg({"messageType": "register", "channelID": add_epoch("CASE_CHAN"), "uaid":""})
        self._compare_dict(ret, {'status': 200, 'messageType':'register'})  
        self._validate_endpoint(ret['pushEndpoint'])

        ret = self._msg({"messageType": "register", "channelID": "", "uaid":"CASE_UAID"})
        self._compare_dict(ret, {'status': 503, 'messageType':'register','error': 'No Channel ID Specified'}) 
        
        # test ping
        # XXX Bug - ping after error isn't updated in response
        # self._msg({"messageType": "ping"})
        # self._compare_dict(ret, {'status': 200, 'messageType': 'ping'}) 

    def test_hello_bad_types(self):
        """ Test handshake messageType with lots of data types """
        for dt in data_types:
            verify_json = {"messageType":"%s"%dt,"status":401, "uaid":"uaid"}
            ret = self._msg({"messageType": '%s' % dt, 
                            "channelIDs": [add_epoch("abc")], "uaid":"uaid"})
            if dt == 'HeLLO':
                verify_json["status"] = 200
            else:
                verify_json["error"] ="Unknown command"
            self._compare_dict(ret, verify_json)

        # sending non strings to make sure it doesn't break server
        self.ws.send('{"messageType": 123}')
        self.assertEqual(self.ws.recv(), None)
        try:
            self.ws.send('{"messageType": null}')
        except Exception, (errno, msg):
            print 'Exception', errno, msg
            self.assertEqual(errno, 32)
            self.assertEqual(msg, 'Broken pipe')

    def test_hello_uaid_types(self):# 
        """ Test handshake uaids with lots of data types """
        for string in strings:
            valid_json = {"messageType":"hello"}
            ws = websocket.create_connection(url)
            msg = {"messageType": "hello",
                   "customKey": "custom value",
                   "channelIDs": [add_epoch("hello_uaid_types")],
                   "uaid": "%s" % string}
            ws.send(json.dumps(msg))
            ret = json.loads(ws.recv())
            if string == "valid_uaid":
                # Spec doesn't support sending hello's, and empty returns last uaid
                valid_json["status"] = 200
                valid_json["uaid"] = "valid_uaid"
            elif string == "":
                valid_json["status"] = 200
                self.assertEqual(len(ret["uaid"]), 32)
            elif string == " fooey barrey ":
                valid_json["status"] = 500
            elif len(string) > 100:
                # 100 char limit for UAID and Channel
                valid_json["status"] = 401
                valid_json["error"] = "An Invalid value was specified"

            self._compare_dict(ret, valid_json)

    def test_hello_invalid_keys(self):
        """ Test various json keys """
        for dt in data_types:
            invalid_ws = websocket.create_connection(url)
            invalid_ws.send(json.dumps({"%s"%dt: "hello"}))
            try:
                ret = json.loads(invalid_ws.recv())
            except Exception as e:
                print 'Exception - Unable to read socket:', e

            if dt == 'messageType':
                self._compare_dict(ret, {"messageType":"hello","status":401,
                                       "error":"Missing required fields for command"})       
            else:
                self._compare_dict(ret, {"status":401,"error":"Unknown command"})       

            invalid_ws.close()

    def test_reg_noshake(self):
        """ Test registration without prior handshake """
        # no handshake invalid
        ret = self._msg({"messageType": "register", "channelID":add_epoch("reg_noshake_chan_1")})
        self._compare_dict(ret, {"messageType":"register","status":401,"error":"Invalid command"})       

        # valid
        ret = self._msg({"messageType": "hello", 
                         "channelIDs": [add_epoch("reg_noshake_chan_1")], "uaid":"reg_noshake_uaid_1"})
        ret = self._msg({"messageType": "register", "channelID":add_epoch("reg_noshake_chan_1")})
        self._compare_dict(ret, {"messageType":"register","status":200})
        self._validate_endpoint(ret['pushEndpoint'])

    def test_reg_duplicate(self):
        """ Test registration with duplicate channel name """
        self._msg({"messageType": "hello", 
                  "channelIDs": [add_epoch("reg_noshake_chan_1")], "uaid":"reg_noshake_uaid_1"})
        ret = self._msg({"messageType": "register", "channelID":add_epoch("dupe_handshake")})
        self._compare_dict(ret, {"messageType":"register","status":200})
        
        # duplicate handshake
        ret = self._msg({"messageType": "register", "channelID":add_epoch("dupe_handshake")})
        self._compare_dict(ret, {"messageType":"register","status":200})

        # passing in list to channelID
        ret = self._msg({"messageType": "register", "channelIDs":[add_epoch("chan_list")]})
        self._compare_dict(ret, {"messageType":"register","status":401,
                               "error":"Missing required fields for command"})

    def test_reg_plural(self):
        """ Test registration with a lot of channels and uaids """
        # XXX bug uaid can get overloaded with channels, adding epoch to unique-ify it.
        self._msg({"messageType": "hello", 
                  "channelIDs": [add_epoch("reg_plural_chan")], "uaid":add_epoch("reg_plural")})
        ret = self._msg({"messageType": "register", 
                        "channelID":add_epoch("reg_plural_chan"), "uaid":add_epoch("reg_plural")})
        
        self._compare_dict(ret, {"messageType":"register","status":200})

        # valid with same channelID
        ret = self._msg({"messageType": "register", "channelID":add_epoch("reg_plural_chan")})
        self._compare_dict(ret, {"messageType":"register","status":200})

        # loop through different channelID values
        for dt in data_types:
            ret = self._msg({"messageType": "register",
                             "channelID":add_epoch("%s")%dt,
                             "uaid":add_epoch("diff_uaid")})
            if 'error' in ret:
                # lots of errors here, lots of gross logic to validate them here
                continue
            self._compare_dict(ret, {"messageType":"register","status":200})

    def test_unreg(self):
        """ Test unregister """
        # unreg non existent
        ret = self._msg({"messageType": "unregister"})
        self._compare_dict(ret, {"messageType":"unregister","status":401,"error":"Invalid command"})   

        # unreg a non existent channel
        ret = self._msg({"messageType": "unregister", "channelID": add_epoch("unreg_chan")})
        self._compare_dict(ret, {"messageType":"unregister","status":401,"error":"Invalid command"})        

        # setup
        self._msg({"messageType": "hello", 
                   "channelIDs": [add_epoch("unreg_chan")], "uaid":"unreg_uaid"})
        self._msg({"messageType": "register", 
                   "channelID":add_epoch("unreg_chan")})
        self._msg({"messageType": "hello", "channelIDs": [add_epoch("unreg_chan")], "uaid":"unreg_uaid"})

        # unreg
        ret = self._msg({"messageType": "unregister", "channelID": add_epoch("unreg_chan")})
        self._compare_dict(ret, {"messageType":"unregister","status":200})  

        # check if channel exists
        ret = self._msg({"messageType": "unregister", "channelID": add_epoch("unreg_chan")})
        # XXX No-op on server results in this behavior
        self._compare_dict(ret, {"messageType":"unregister","status":200}) 

    def test_chan_limits(self):
        """ Test string limits for keys """
        self._msg({"messageType": "hello", 
                  "channelIDs": [add_epoch("chan_limits")], "uaid":"chan_limit_uaid"})

        ret = self._msg({"messageType": "register", "channelID":"%s" % chan_150[:101]})
        self._compare_dict(ret, {"status": 401, "messageType": "register", 
                               "error": "An Invalid value was specified"})

        ret = self._msg({"messageType": "register", "channelID":"%s" % chan_150[:100]})
        self._compare_dict(ret, {"status": 200, "messageType": "register"})
        self._validate_endpoint(ret['pushEndpoint'])

        # hello 100 channels
        ret = self._msg({"messageType": "hello", "channelIDs": big_uuid, "uaid":"chan_limit_uaid"})
        self._compare_dict(ret, {"status": 200, "messageType": "hello"})

        # register 100 channels
        for chan in big_uuid:
            ret = self._msg({"messageType": "register", "channelID": chan, "uaid":"chan_limit_uaid"})
            self._compare_dict(ret, {"status": 200, "messageType": "register"})
            self._validate_endpoint(ret['pushEndpoint'])

    def test_ping(self):
        # happy
        ret = self._msg({'messageType':'ping'})
        self._compare_dict(ret, {"messageType":"ping","status":200}) 

        # extra args
        ret = self._msg({'messageType':'ping', 'channelIDs':['ping_chan'], 'uaid':'ping_uaid','nada':''})
        self._compare_dict(ret, {"messageType":"ping","status":200}) 

        # do a register between pings
        self._msg({"messageType": "hello", 
                  "channelIDs": [add_epoch("ping_chan_1")], "uaid":"ping_uaid"})
        ret = self._msg({"messageType": "register", "channelID":add_epoch("ping_chan_1")})
        self._compare_dict(ret, {"status": 200, "messageType": "register"})
        # send and ack too
        # XXX ack can hang socket
        # ret = self._msg({"messageType": "ack", 
        #                 "updates": [{ "channelID": add_epoch("ping_chan_1"), 
        #                 "version": 123 }]})
        # self._compare_dict(ret, {"status": 200, "messageType": "ack"})

        # empty braces is a valid ping
        ret = self._msg({})
        self._compare_dict(ret, {"messageType":"ping","status":200}) 

        for ping in range(100):
            ret = self._msg({'messageType':'ping'})
            self._compare_dict(ret, {"messageType":"ping","status":200}) 

    def test_ack(self):
        """ Test ack """
        # no hello
        ret = self._msg({"messageType": "ack", 
                        "updates": [{ "channelID": add_epoch("ack_chan_1"), "version": 23 }]})
        self._compare_dict(ret, {"error":"Invalid command", "status": 401, "messageType": "ack"})
        self.assertEqual(ret["updates"][0]["channelID"], add_epoch("ack_chan_1"))
        self.assertEqual(ret["updates"][0]["version"], 23)

        # happy path
        self._msg({"messageType": "hello", 
                  "channelIDs": [add_epoch("ack_chan_1")], "uaid":"ack_uaid"})
        reg = self._msg({"messageType": "register", "channelID":add_epoch("ack_chan_1")})

        # send an http PUT request to the endpoint 
        send_http_put(reg['pushEndpoint'])

        # this blocks the socket on read
        # print 'RECV', self.ws.recv()
        # hanging socket against AWS
        ret = self._msg({"messageType": "ack", 
                        "updates": [{ "channelID": add_epoch("ack_chan_1"), "version": 23 }]})
        self._compare_dict(ret, {"messageType":"notification", "expired": None}) 
        self.assertEqual(ret["updates"][0]["channelID"], add_epoch("ack_chan_1"))

    def tearDown(self):
        self.ws.close()

if __name__ == '__main__':
    unittest.main(verbosity=2)
