import assert from 'node:assert/strict';
import { once } from 'node:events';
import { createConnection } from 'node:net';
import { test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { ProcessSupervisor } from '../supervisor.mjs';

test('child failure is reported and sibling processes can be cleaned up', async () => {
  let reportFailure;
  const failure = new Promise((resolve) => {
    reportFailure = resolve;
  });
  const supervisor = new ProcessSupervisor(reportFailure);
  const sibling = supervisor.start(
    process.execPath,
    ['-e', 'setInterval(() => {}, 1000)'],
    { stdio: 'ignore' },
  );
  try {
    supervisor.start(process.execPath, ['-e', 'process.exit(7)'], {
      stdio: 'ignore',
    });
    assert.match((await failure).message, /7/);
  } finally {
    await supervisor.stop(1_000);
  }
  assert.equal(sibling.exited, true);
});

test('missing executables fail without an unhandled error', async () => {
  const supervisor = new ProcessSupervisor();
  try {
    await assert.rejects(
      supervisor.run('portway-missing-test-executable', [], {
        stdio: 'ignore',
      }),
      /failed/,
    );
  } finally {
    await supervisor.stop();
  }
});

test('shutdown stops a package-manager style grandchild and releases its port', async () => {
  const supervisor = new ProcessSupervisor();
  const program = `
    const { spawn } = require('node:child_process');
    spawn(process.execPath, ['-e', \`
      const server = require('node:net').createServer();
      server.listen(0, '127.0.0.1', () => console.log(server.address().port));
    \`], { stdio: ['ignore', 'inherit', 'inherit'] });
    setInterval(() => {}, 1000);
  `;
  const child = supervisor.start(process.execPath, ['-e', program], {
    stdio: ['ignore', 'pipe', 'ignore'],
  });
  try {
    const [chunk] = await once(child.child.stdout, 'data', {
      signal: AbortSignal.timeout(5_000),
    });
    const port = Number(String(chunk).trim());
    assert.ok(port > 0);
    await supervisor.stop(1_000);
    // Give the OS a short bounded interval to release the listening socket.
    for (let attempt = 0; attempt < 20; attempt++) {
      if (!(await reachable(port))) return;
      await delay(25);
    }
    assert.fail('Grandchild port remained open after shutdown');
  } finally {
    await supervisor.stop(1_000);
  }
});

function reachable(port) {
  return new Promise((resolve) => {
    const socket = createConnection({ host: '127.0.0.1', port });
    socket.once('connect', () => {
      socket.destroy();
      resolve(true);
    });
    socket.once('error', () => resolve(false));
    socket.setTimeout(250, () => {
      socket.destroy();
      resolve(false);
    });
  });
}
