// Preload shim: Node 18+ removed the native `http_parser` binding (`process.binding('http_parser')`
// throws). Legacy deps still bundled by umi's esmi plugin (http-deceiver / spdy) call it at require
// time. This shim installs a minimal stand-in so those modules load; actual HTTP parsing in this
// build context is done by Node's modern `http` module, not by spdy's deceiver path, so the stand-in
// only needs to satisfy the constant-destructuring shape, not be a real parser.
const dummyMethods = [
  'DELETE',
  'GET',
  'HEAD',
  'POST',
  'PUT',
  'CONNECT',
  'OPTIONS',
  'TRACE',
  'COPY',
  'LOCK',
  'MKCOL',
  'MOVE',
  'PROPFIND',
  'PROPPATCH',
  'SEARCH',
  'UNLOCK',
  'BIND',
  'REBIND',
  'UNBIND',
  'ACL',
  'REPORT',
  'MKACTIVITY',
  'CHECKOUT',
  'MERGE',
  'M-SEARCH',
  'NOTIFY',
  'SUBSCRIBE',
  'UNSUBSCRIBE',
  'PATCH',
  'PURGE',
  'MKCALENDAR',
  'LINK',
  'UNLINK',
];
const fakeHTTPParser = {
  HTTPParser: {
    REQUEST: 0,
    RESPONSE: 1,
    methods: dummyMethods,
    kOnHeaders: 0,
    kOnHeadersComplete: 1,
    kOnMessageComplete: 2,
    kOnBody: 3,
  },
  methods: dummyMethods,
};

const origBinding = process.binding;
process.binding = function binding(name) {
  if (name === 'http_parser') {
    return fakeHTTPParser;
  }
  return origBinding.call(process, name);
};
