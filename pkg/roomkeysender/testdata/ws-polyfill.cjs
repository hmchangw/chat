// WebSocket polyfill for Node.js environments that lack a native WebSocket implementation.
// nats.ws requires globalThis.WebSocket; Node 20 does not provide it by default.
// The 'websocket' npm package (installed globally in the test image) supplies a W3C-compatible
// implementation via its w3cwebsocket class.
globalThis.WebSocket = require("websocket").w3cwebsocket;
