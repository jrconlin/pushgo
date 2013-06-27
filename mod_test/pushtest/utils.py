import json, string, random, re, sys, time, urllib2

def str_gen(size=6, chars=string.ascii_uppercase + string.digits):
    #generate rand string
    return ''.join(random.choice(chars) for x in range(size))

def str2bool(v):
  return v.lower() in ("true", "1")

def print_log(prefix, msg):
    print "::%s: %s" % (prefix, msg)

def get_uaid(chan_str):
    """uniquify our channels so there's no collision"""
    return "%s%s" % (chan_str, str_gen(16))

def send_http_put(update_path, args='version=123'):
    """ executes an HTTP PUT with version"""
    print_log('update_path', update_path)
    opener = urllib2.build_opener(urllib2.HTTPHandler)
    request = urllib2.Request(update_path, data=args)
    request.get_method = lambda: 'PUT'
    try:
        url = opener.open(request)
    except HTTPError, (errno, msg):
        raise
    url.close()
    return url.getcode()

def comp_dict(ret_data, exp_data):
    """ Util that compares dicts returns list of errors"""
    diff = {"errors":[]}
    for key in exp_data:
        if key not in ret_data:
            diff["errors"].append("%s not in %s" % (key, ret_data))
            continue
        if ret_data[key] != exp_data[key]:
            diff["errors"].append("'%s:%s' not in '%s'" % (key, exp_data[key], ret_data))
    return diff

def get_endpoint(ws_url):
    """ takes a websocket and returns http path"""
    if 'wss:' in ws_url:
        ret =  ws_url.replace('wss:', 'https:')
    else:
        ret = ws_url.replace('ws:', 'http:')
    return ret