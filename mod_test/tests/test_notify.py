#!/usr/bin/python

# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/.

import json
import unittest
import websocket

from pushtest.pushTestCase import PushTestCase
from pushtest.utils import (get_uaid, send_http_put)


class TestNotify(PushTestCase):
    """ Test HTTP PUT and notifications generated tests """
    def setUp(self):
        if self.debug:
            websocket.enableTrace(True)

    def test_two_chan(self):
        self.chan1 = ""
        self.chan2 = ""
        self.uaid = get_uaid('notify')

        def on_close(ws):
            self.log('on_close:')

        def _assert_equal(a, b):
            """ Running socket swallows asserts, force an exit on mismatch """
            if a != b:
                self.log('ERROR: value mismatch:', '%s != %s' % (a, b))
                exit('assert equal mismatch: %s != %s' % (a, b))

        def _check_updates(updates_list, chan1_val="", chan2_val=""):
            for chan in updates_list:
                if chan1_val != "" and chan["channelID"] == "tn.chan1":
                    _assert_equal(chan["version"], chan1_val)
                if chan2_val != "" and chan["channelID"] == "tn.chan2":
                    _assert_equal(chan["version"], chan2_val)

            return

        def on_message(ws, message):
            ret = json.loads(message)
            if ws.state == 'purge':
                ws.close()
                return
            if len(ret) == 0:
                # skip blank records
                return
            self.log('on_msg:', ret)
            self.log('state', ws.state)

            if ws.state == 'hello':
                reg_chan(ws, 'register1', 'tn_chan1')
            elif ws.state == 'register1':
                # register chan1
                self.chan1 = ret.get("pushEndpoint")
                self.log('self.chan1', self.chan1)
                reg_chan(ws, 'register2', 'tn_chan2')
            elif ws.state == 'register2':
                # register chan2
                self.chan2 = ret.get("pushEndpoint")
                self.log('self.chan2', self.chan2)
                # http put to chan1
                send_update(self.chan1, 'version=12345789', 'update1')
            elif ws.state == 'update1':
                #verify chan1
                self.compare_dict(ret, {"messageType": "notification"}, True)
                _assert_equal(ret["updates"][0]["channelID"], "tn_chan1")
                _check_updates(ret["updates"], 12345789)

                # http put to chan2
                send_update(self.chan2, 'version=987654321', 'update2')
            elif ws.state == 'update2':
                #verify chan1 and chan2
                self.compare_dict(ret, {"messageType": "notification"}, True)
                _check_updates(ret["updates"], 12345789, 987654321)
                send_ack(ws, ret["updates"])

                # http put to chan1
                send_update(self.chan1, 'version=1', 'update3')
            elif ws.state == 'update3':
                # update the same channel
                self.compare_dict(ret, {"messageType": "notification"}, True)
                _check_updates(ret["updates"], 1)
                send_ack(ws, ret["updates"])

                # http put to chan2
                send_update(self.chan2, 'version=0', 'update4', 'text/plain')
            elif ws.state == 'update4':
                # update the same channel
                self.compare_dict(ret, {"messageType": "notification"}, True)
                for chan in ret["updates"]:
                    if chan["channelID"] == "tn.chan2":
                        # check if 0 version returns epoch
                        _assert_equal(len(str(chan["version"])), 10)

                send_ack(ws, ret["updates"])

                # http put to chan2 with invalid content-type, results in epoch
                send_update(self.chan1, 'version=999',
                            'update5', 'application/json')
            elif ws.state == 'update5':
                # update the same channel
                self.compare_dict(ret, {"messageType": "notification"}, True)
                for chan in ret["updates"]:
                    if chan["channelID"] == "tn.chan1":
                        # check if 0 version returns epoch
                        _assert_equal(len(str(chan["version"])), 10)

                send_ack(ws, ret["updates"])
                ws.state="purge"
                self.msg(ws, {"messageType": "purge"})

        def on_open(ws):
            self.log('open:', ws)
            setup_chan(ws)

        def on_error(ws):
            self.log('on error')
            ws.close()
            raise AssertionError(ws)

        def setup_chan(ws):
            ws.state = 'hello'
            self.msg(ws,
                     {"messageType": "hello",
                      "channelIDs": [],
                      "uaid": self.uaid},
                     cb=False)
            self.msg(ws, {"messageType": "purge"})

        def reg_chan(ws, state, chan_str):
            ws.state = state
            self.msg(ws,
                     {"messageType": "register",
                      "channelID": chan_str,
                      "uaid": self.uaid},
                     cb=False)

        def send_ack(ws, updates_list):
            # there should be no response from ack
            self.log('Sending ACK')
            self.msg(ws,
                     {"messageType": "ack",
                      "updates": updates_list,
                      "uaid": self.uaid},
                     cb=False)

        def send_update(update_url, str_data, state='update1',
                        ct='application/x-www-form-urlencoded'):
            if update_url == '':
                import pdb; pdb.set_trace()
                raise AssertionError("No update_url found")
            ws.state = state
            resp = send_http_put(update_url, str_data, ct, True)
            _assert_equal(resp, 200)

        ws = websocket.WebSocketApp(self.url,
                                    on_open=on_open,
                                    on_message=on_message,
                                    on_error=on_error,
                                    on_close=on_close)
        ws.run_forever()

    def tearDown(self):
        pass

if __name__ == '__main__':
    unittest.main(verbosity=2)
