import websocket

import json
import ConfigParser
import httplib
import urlparse
import pprint

appid = "test1"

def on_close(ws):
    print "## Closed"


def on_error(ws, error):
    print "## Error:: " + error
    exit()


def on_message(ws, message):


    print "<<< Recv'd:: " + ws.state + ">> " + message
    msg = json.loads(message)
    print "<<< " + pprint.pformat(msg)
    type = msg.get("messageType")
    if type is None:
        exit("Unknown message type sent")
    if msg.get("status") is not None:
        ## We have a status element, this is a response field.
        if msg.get("status") != 200:
            ## Normally 200 is success, but because of testing, the channel
            ## may already exist. If so, unregister it and try again.
            if ws.state == 'register':
                ## The test channel is already registered. Clear it and
                ## try again.
                ws.state = "helloagain"
                send_unreg(ws)
                return
    else:
        if type == "notification":
            if ws.state == "update":
                check_update(msg)
                print "### Update pass, shutting down...."
                ## disable this if you want to test multiple messages.
                ws.state = "shutdown"
                send_unreg(ws)
            send_ack(ws, msg)
            return
    if ws.state == "helloagain":
        # retry the registration
        ws.state = "register"
        ws.send(json.dumps({"messageType": ws.state, "channelID": appid}))
        return
    if ws.state == "hello":
        ## We're recognized, try to register the appid channel.
        ## NOTE: Normally, channelIDs are UUID4 type values.
        check_hello(msg)
        ws.state = "register"
        ws.send(json.dumps({"messageType": ws.state, "channelID": appid}))
        return
    if ws.state == "register":
        ## Endpoint is registered. Send an update via the REST interface.
        check_register(msg)
        ws.update_url = msg.get("pushEndpoint")
        ## Look for an Update.
        ws.state = "update"
        send_rest_alert(ws)
        return
    if ws.state == "shutdown":
        print "### Exiting..."
        exit()


def on_open(ws):
    ws.state = "hello"
    print ">>> Sending 'Hello'"
    ws.send(json.dumps({"messageType": ws.state, "uaid": "test"}))


def check_hello(msg):
    try:
        assert(msg.get("uaid") == "test", "did not echo UAID")
    except AssertionError, e:
        print e
        exit("Hello failed check")
    return


def check_update(msg):
    try:
        assert(msg.get("updates")[0].get("channelID") == appid,
               "does not contain channelID")
    except AssertionError, e:
        print e
        exit("Update failed check")
    return


def check_register(msg):
    try:
        assert(msg.get("pushEndpoint") is not None, "Missing pushEndpoint")
    except AssertionError, e:
        print e
        exit("Register Failed")
    return


def send_rest_alert(ws):
    print ">>> Sending REST update"
    url = urlparse.urlparse(ws.update_url)
    http = httplib.HTTPConnection(url.netloc)
    req = http.request("PUT", url.path)
    print "#>> " + pprint.pformat(req)
    resp = http.getresponse()
    if resp.status != 200:
        exit("invalid url")
    print "#<< " + pprint.pformat(resp.read())


def send_unreg(ws):
    print ">>> Sending Unreg"
    ws.send(json.dumps({"messageType": "unregister", "channelID": appid}))


def send_ack(ws, msg):
    msg['messageType'] = "ack"
    print ">>> send ack" + json.dumps(msg)
    ws.send(json.dumps(msg))


def main():
    config = ConfigParser.ConfigParser()
    config.read('config.ini')

    url = config.get('server', 'url')

    websocket.enableTrace(config.get('debug', 'trace'))

    ws = websocket.WebSocketApp(url,
                                on_open=on_open,
                                on_message=on_message,
                                on_error=on_error,
                                on_close=on_close)

    ws.run_forever()
    print("leaving")

main()
