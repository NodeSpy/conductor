// The `conductor` provider. Its ACP stream is the conductor daemon's socket:
// for each paseo session, conductor launches the agent itself — on the acp
// runtime its paseo_provider block names, in conductor's workspace jail —
// and relays ACP both ways. paseo never holds the agent process.
//
// This file is embedded in the conductor binary and written out, with
// server/endpoint.json beside it, by `conductor paseo install-plugin`; edit it in
// the conductor repo (internal/paseoplugin), never in place.
import type { PluginServerContext } from "@getpaseo/plugin/server";
import { runAcpProvider } from "@getpaseo/plugin/server/acp";
import type { AcpStream, AcpStreamMessage } from "@getpaseo/plugin/server/acp";
import type {
  ProviderConnection,
  ProviderEvent,
  ProviderInput,
  ProviderRegistration,
} from "@getpaseo/plugin/server/provider";
import { connect } from "node:net";
import endpoint from "./server/endpoint.json";

const PROVIDER_ID = "conductor";

// socketStream opens one ACP stream to conductor: newline-delimited JSON-RPC
// over its unix socket.
function socketStream(): Promise<AcpStream> {
  return new Promise((resolve, reject) => {
    const sock = connect(endpoint.socket);
    sock.once("error", reject);
    sock.once("connect", () => {
      sock.off("error", reject);
      let buffered = "";
      const readable = new ReadableStream<AcpStreamMessage>({
        start(controller) {
          sock.on("data", (chunk) => {
            buffered += chunk.toString("utf8");
            let nl: number;
            while ((nl = buffered.indexOf("\n")) >= 0) {
              const line = buffered.slice(0, nl).trim();
              buffered = buffered.slice(nl + 1);
              if (line) controller.enqueue(JSON.parse(line) as AcpStreamMessage);
            }
          });
          sock.on("end", () => controller.close());
          sock.on("error", (err) => controller.error(err));
        },
        cancel() {
          sock.destroy();
        },
      });
      const writable = new WritableStream<AcpStreamMessage>({
        write(message) {
          return new Promise<void>((done, fail) =>
            sock.write(JSON.stringify(message) + "\n", (err) => (err ? fail(err) : done())),
          );
        },
        close() {
          sock.end();
        },
        abort() {
          sock.destroy();
        },
      });
      resolve({ readable, writable });
    });
  });
}

// control makes one conductor control call (not ACP) on a fresh connection.
async function control(method: string, params: Record<string, unknown>): Promise<void> {
  const stream = await socketStream();
  const writer = stream.writable.getWriter();
  const reader = stream.readable.getReader();
  try {
    await writer.write({ jsonrpc: "2.0", id: 1, method, params });
    await reader.read();
  } finally {
    reader.releaseLock();
    await writer.close().catch(() => undefined);
  }
}

// persistedSession is conductor's session id in a provider persistence.
function persistedSession(persistence: { data: unknown } | undefined): string | undefined {
  const data = persistence?.data;
  if (data && typeof data === "object" && !Array.isArray(data)) {
    const id = (data as Record<string, unknown>).sessionId;
    if (typeof id === "string") return id;
  }
  return undefined;
}

// withIdentity wraps the ACP provider so conductor knows which paseo agent
// each session is: the agent id (paseo's launch env carries it, but env
// never crosses an ACP stream) and paseo's own session id go to conductor in
// providerOptions._conductor, which the ACP adapter forwards in
// session/new|load `_meta`. It also serves archive, which the ACP adapter
// cannot: conductor tears down the agent's jail.
function withIdentity(inner: ProviderRegistration): ProviderRegistration {
  return {
    ...inner,
    async connect(request) {
      const wantsArchive = request.capabilities.includes("session.archive");
      const conn: ProviderConnection = await inner.connect(request);
      const listeners = new Set<(event: ProviderEvent) => void>();
      const emit = (event: ProviderEvent) => {
        for (const listener of listeners) listener(event);
      };
      const unsubscribe = conn.onEvent(emit);
      const capabilities = [...conn.capabilities];
      if (wantsArchive) capabilities.push("session.archive", "session.unarchive");
      return {
        version: conn.version,
        capabilities,
        onEvent(listener) {
          listeners.add(listener);
          return () => listeners.delete(listener);
        },
        async close() {
          unsubscribe();
          await conn.close();
        },
        async send(input: ProviderInput) {
          if (input.type === "session.archive") {
            const sessionId = persistedSession(input.persistence);
            try {
              if (sessionId) await control("_conductor/archive", { sessionId });
              emit({ type: "request.completed", requestId: input.requestId } as ProviderEvent);
            } catch (err) {
              emit({
                type: "request.failed",
                requestId: input.requestId,
                error: { message: `conductor: ${String(err)}` },
              } as ProviderEvent);
            }
            return;
          }
          if (input.type === "session.unarchive") {
            // Nothing to restore: the next session.open relaunches the agent.
            emit({ type: "request.completed", requestId: input.requestId } as ProviderEvent);
            return;
          }
          if (input.type === "session.open") {
            input = {
              ...input,
              config: {
                ...input.config,
                providerOptions: {
                  ...(input.config.providerOptions ?? {}),
                  _conductor: {
                    agentId: input.config.env.PASEO_AGENT_ID ?? null,
                    paseoSession: input.sessionId,
                  },
                },
              },
            };
          }
          return conn.send(input);
        },
      };
    },
  };
}

export default function contribute(server: PluginServerContext) {
  server.registerProvider(
    withIdentity(
      runAcpProvider({
        id: PROVIDER_ID,
        label: "Conductor",
        description: "Agents launched and jailed by conductor",
        connector: socketStream,
        acpOptions: { startupTimeoutMs: 120000 },
      }),
    ),
  );
  // An archived agent's jail goes now, whichever path paseo archived it by.
  const off = server.on("agent.archived", async ({ agent }) => {
    if (agent.provider !== PROVIDER_ID) return;
    await control("_conductor/archive", { agentId: agent.id }).catch(() => undefined);
  });
  return () => off();
}
