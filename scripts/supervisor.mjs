import { spawn, spawnSync } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';

// Each child gets its own process group so package-manager grandchildren are
// included in shutdown. No shared Docker daemon or unrelated process is stopped.
export class ProcessSupervisor {
  #records = [];
  #stopping = false;
  #onFailure;

  constructor(onFailure = () => {}) {
    this.#onFailure = onFailure;
  }

  start(
    command,
    args,
    { cwd, env = process.env, persistent = true, stdio = 'inherit' } = {},
  ) {
    if (this.#stopping)
      throw new Error('Development startup has been canceled');
    const child = spawn(command, args, {
      cwd,
      env,
      stdio,
      detached: process.platform !== 'win32',
    });
    const record = { child, exited: false, done: null };
    record.done = new Promise((resolve) => {
      const finish = (code, error) => {
        if (record.exited) return;
        record.exited = true;
        resolve({ code, error });
        if (persistent && !this.#stopping) {
          this.#onFailure(
            error ??
              new Error(`${command} exited unexpectedly (${code ?? 'signal'})`),
          );
        }
      };
      child.once('error', (error) => finish(null, error));
      child.once('exit', (code) => finish(code, null));
    });
    this.#records.push(record);
    return record;
  }

  async run(command, args, options = {}) {
    const record = this.start(command, args, { ...options, persistent: false });
    const { code, error } = await record.done;
    if (error || code !== 0)
      throw new Error(`${command} failed (${code ?? 'signal'})`, {
        cause: error,
      });
  }

  async stop(timeoutMs = 5_000) {
    this.#stopping = true;
    for (const record of this.#records) signalGroup(record.child, 'SIGTERM');
    await Promise.race([
      Promise.all(this.#records.map((record) => record.done)),
      delay(timeoutMs, undefined, { ref: false }),
    ]);
    // A parent may have exited while a grandchild is still alive.
    for (const record of this.#records) signalGroup(record.child, 'SIGKILL');
    await Promise.all(this.#records.map((record) => record.done));
  }
}

function signalGroup(child, signal) {
  if (!child.pid) return;
  try {
    if (process.platform === 'win32') {
      spawnSync('taskkill', ['/pid', String(child.pid), '/T', '/F'], {
        stdio: 'ignore',
        timeout: 5_000,
      });
    } else {
      process.kill(-child.pid, signal);
    }
  } catch (error) {
    if (error.code !== 'ESRCH') throw error;
  }
}
