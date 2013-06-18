import json, string, random, re, sys, time, urllib2

epoch = int(time.time())

def str_gen(size=6, chars=string.ascii_uppercase + string.digits):
    #generate rand string
    return ''.join(random.choice(chars) for x in range(size))

def str2bool(v):
  return v.lower() in ("true", "1")

def log(prefix, msg):
    print "::%s: %s" % (prefix, msg)

def add_epoch(chan_str):
    """uniquify our channels so there's no collision"""
    return "%s%s" % (chan_str, epoch)

def send_http_put(update_path, args='version=123'):
    """ executes an HTTP PUT with version"""
    opener = urllib2.build_opener(urllib2.HTTPHandler)
    request = urllib2.Request(update_path, data=args)
    request.get_method = lambda: 'PUT'
    url = opener.open(request)
    url.close()
    return url.getcode()

def compare_dict(ret_data, exp_data):
    """ Util that compares dicts returns list of errors"""
    diff = {"errors":[]}
    for key in exp_data:
        if key not in ret_data:
            diff["errors"].append("%s not in %s" % (key, ret_data))
            break
        if ret_data[key] != exp_data[key]:
            diff["errors"].append("'%s:%s' not in '%s'" % (key, exp_data[key], ret_data))
    return diff

def get_endpoint(ws):
    """ takes a websocket and returns http path"""
    if ws.find('wss:'):
        return ws.replace('wss:', 'https:')
    else:
        return ws.replace('ws:', 'http:')
