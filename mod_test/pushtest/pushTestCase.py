
import ConfigParser, json, unittest, websocket
from utils import *

class PushTestCase(unittest.TestCase):
    """ General API tests """
    config = ConfigParser.ConfigParser()
    config.read('../config.ini')
    url = config.get('server', 'url')
    debug = str2bool(config.get('debug', 'trace'))
    verbose = str2bool(config.get('debug', 'verbose'))

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

    def __init__(self, *args, **kwargs):
        unittest.TestCase.__init__(self, *args, **kwargs)
        
    def log(self, prefix, msg = ""):
        if self.verbose:
            print_log(prefix, msg)

    def msg(self, ws, msg, cb='cb'):
        """ Util that sends and returns dict"""
        self.log("SEND:", msg)
        try:
            ws.send(json.dumps(msg))
        except Exception as e:
            print 'Unable to send data', e
            return None
        if cb:
            try:
                ret = ws.recv()
                return json.loads(ret)
            except Exception, e:
                print 'Unable to parse json:', e
                raise AssertionError, e

    def compare_dict(self, ret_data, exp_data):
        """ compares two dictionaries and raises assert with info """
        self.log("RESPONSE GOT:", ret_data)
        self.log("RESPONSE EXPECTED:", exp_data)
   
        diff = comp_dict(ret_data, exp_data)

        if diff["errors"]:
            print 'AssertionError', diff["errors"]
            raise AssertionError, diff["errors"]

    def validate_endpoint(self, endpoint):
        """ validate endpoint is in proper url format """
        self.assertRegexpMatches(endpoint, r'(http|https)://.*/update/.*')