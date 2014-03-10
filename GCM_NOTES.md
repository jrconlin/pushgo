Google Cloud Messaging (GCM) support:
===

GCM is significantly more capable than SimplePush, which is a good
thing, and we can take full advantage of this fact.

GCM Documentation:
<http://developer.android.com/google/gcm/index.html>

In order to use GCM, you'll need to configure the following:

1. Go to https://console.developers.google.com/project (Your account
will need project creation privileges.)

1.a If you don't already have a project, create a new one. (Note, this
may become a billable expense.)

1.a.1 Store the unique project id in the "config.ini" as
gcm.project_id

1.b For the project, click on the "APIs & Auth" section

1.c Under "APIs" find "Google Cloud Messaging for Android" and
activate it

1.d Under "Credentials" find Public API access, and create a new key

1.d.1 You may wish to set the IPs to a limited set that includes your
SimplePush server (this limits who can send GCM requests)

1.d.2 Note the API Key that's generated. Store that value in the
"config.ini" as gcm.api_key

1.e You may wish to also set gcm.dry_run if you don't wish GCM to send
out notifications to devices.


Client Use
---
On the client side, an app will call the
GoogleCloudMessaging.getInstance(this).register(SENDER_ID) and get the
registration id. (See the GCM sample programs for how to best do this.
<http://developer.android.com/google/gcm/client.html>)

Once the client has the registration id, it includes it in the "hello"
packet to the server. For example, presuming that the returned
registration ID is the fictitious "abc-registration-id":

    {"msg":"hello",
     "uaid": ""
     "channelIDs": [],
     "connect":{"type":"gcm", "regid":"abc-registration-id"}
    }

The server will then send a "message-to-sync" GCM notification to the
client. This notification is currently data free.

Additional Notes
---
The "Complimentary" (free) GCM service allows for 1 request per second
per user, with a maximum of 1,000,000 requests per day total. This is
enough for small projects and development, but if you plan on running
any service beyond a few hundred users, you're going to burn through
this limit fairly quickly.
