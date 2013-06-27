#!/usr/bin/python

import json, unittest, websocket

from pushtest.pushTestCase import PushTestCase
from pushtest.utils import *

class TestNotify(PushTestCase):
    """ Test HTTP PUT and notifications generated tests """
    def setUp(self):
        if self.debug:
            websocket.enableTrace(True)

    def test_two_chan(self):
        # PUT to unregistered endpoint
        def on_close(ws):
            self.log('on_close:')

        def on_message(ws, message):
            ret = json.loads(message)
            self.log('on_msg:', ret)
            self.log('state', ws.state)
            if ws.state == 'hello':
                reg_chan(ws)
            elif ws.state == 'register':
                ws.update_url = ret.get("pushEndpoint")
                self.log('update_url', ws.update_url)
                send_update_1(ws.update_url)
            elif ws.state == 'update1':
                self.compare_dict(ret, {"messageType":"notification"})
                self.assertEqual(ret["updates"][0]["channelID"], "chan1")
                for chan in ret["updates"]:
                    if chan["channelID"] == "chan1":
                        self.assertEqual(chan["version"], 98764321)
                send_update_2(ws.update_url)
            elif ws.state == 'update2':
                self.compare_dict(ret, {"messageType":"notification"})
                self.assertEqual(len(ret["updates"]), 2)
                for chan in ret["updates"]:
                    if chan["channelID"] == "chan1_diff":
                        self.assertEqual(chan["version"], 123)
                    elif chan["channelID"] == "chan1":
                        self.assertEqual(chan["version"], 98764321)
                send_update_3(ws.update_url)
            elif ws.state == 'update3':
                # update the same channel
                self.compare_dict(ret, {"messageType":"notification"})
                for chan in ret["updates"]:
                    if chan["channelID"] == "chan1":
                        self.assertEqual(chan["version"], 98764322)
                    else:
                        # old chan_diff version is the same
                        self.assertEqual(chan["version"], 123)
                send_update_4(ws.update_url)
            elif ws.state == 'update4':
                # update the same channel
                self.compare_dict(ret, {"messageType":"notification"})
                # invalid version string results in epoch
                for chan in ret["updates"]:
                    if chan["channelID"] == "chan1_invalid":
                        self.assertEqual(len(str(chan["version"])), 10)
                send_ack(ws, 'ack1', ret["updates"])
                ws.close()
            # XXX Bug here with ack doesn't respond
            # elif ws.state == 'ack1':
            #     self.compare_dict(ret, {"messageType":"notification"})
            #     # self.compare_dict(ret["updates"][0], {"channelID":"chan1"})
            #     ws.close()

        def on_open(ws):
            self.log('open:', ws)
            setup_chan(ws)

        def on_close(ws):
            self.log('on close')

        def setup_chan(ws):
            ws.state = 'hello'
            self.msg(ws, 
                     {"messageType": "hello", 
                      "channelIDs": ["chan1", "chan2"],
                      "uaid":get_uaid("notify_uaid")},
                      cb=False)

        def reg_chan(ws):
            ws.state = 'register'
            self.msg(ws, 
                     {"messageType": "register", 
                      "channelID": "chan1",
                      "uaid":get_uaid("notify_uaid")},
                      cb=False)

        def send_ack(ws, ack_str, updates_list):
            # there should be no response from ack
            ws.state = ack_str
            self.log('Sending ACK')
            self.msg(ws, 
                     {"messageType": "ack", 
                      "updates": updates_list,
                      "uaid":get_uaid("notify_uaid")},
                      cb=False)

        def send_update_1(update_url):
            ws.state = 'update1'
            resp = send_http_put(ws.update_url, 'version=98764321')
            self.assertEqual(resp, 200)

        def send_update_2(update_url):
            ws.state = 'update2'
            resp = send_http_put(ws.update_url+'_diff', 'version=123')
            self.assertEqual(resp, 200)

        def send_update_3(update_url):
            ws.state = 'update3'
            resp = send_http_put(ws.update_url, 'version=98764322')
            self.assertEqual(resp, 200)

        def send_update_4(update_url):
            ws.state = 'update4'
            send_http_put(ws.update_url, '')

        ws = websocket.WebSocketApp(self.url,
                                    on_open=on_open,
                                    on_message=on_message,
                                    on_close=on_close)
        ws.run_forever()

    def tearDown(self):
        pass

if __name__ == '__main__':
    unittest.main(verbosity=2)