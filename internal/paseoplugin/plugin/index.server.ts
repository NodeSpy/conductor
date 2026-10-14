// paseo's `conductor` provider: an ACP provider whose stream is the conductor
// daemon's socket. conductor launches each session's agent in its workspace
// jail and relays ACP; everything else lives there. Public plugin API only —
// keep it that way (docs/wiki/Paseo-Provider.md lists the surface it uses).
import type { PluginServerContext } from "@getpaseo/plugin/server";
import { runAcpProvider, type AcpStream } from "@getpaseo/plugin/server/acp";
import { connect } from "node:net";
import endpoint from "./server/endpoint.json";

// One ACP stream: newline-delimited JSON-RPC over conductor's unix socket.
const conductorStream = (): Promise<AcpStream> =>
  new Promise((resolve, reject) => {
    const sock = connect(endpoint.socket, () => {
      let buf = "";
      const readable = new ReadableStream({
        start(c) {
          sock.on("data", (d) => {
            const lines = (buf += d).split("\n");
            buf = lines.pop() ?? "";
            for (const l of lines) if (l.trim()) c.enqueue(JSON.parse(l));
          });
          sock.on("end", () => c.close()).on("error", (e) => c.error(e));
        },
        cancel: () => void sock.destroy(),
      });
      const writable = new WritableStream({
        write: (m) => new Promise<void>((ok, fail) => sock.write(JSON.stringify(m) + "\n", (e) => (e ? fail(e) : ok()))),
        close: () => void sock.end(),
        abort: () => void sock.destroy(),
      });
      resolve({ readable, writable });
    }).once("error", reject);
  });

export default function contribute(server: PluginServerContext) {
  server.registerProvider(runAcpProvider({ id: "conductor", label: "Conductor", connector: conductorStream }));
  return () => {};
}
