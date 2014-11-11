using Go = import "go.capnp";
$Go.package("simplepush");

@0xbe4ec8aa5cee130b;

struct Routable {
  channelID @0 :Text;
  version @1 :Int64;
  time @2 :Int64;
}
