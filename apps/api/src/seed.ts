import { constants, closeSync, fstatSync, openSync, readSync } from 'node:fs';
import type { Seed } from './models.ts';
import { parseObject } from './validation.ts';

export function loadSeed(path: string): Seed | undefined {
  if (!path) return undefined;
  let fd: number | undefined;
  try {
    fd = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0));
    const info = fstatSync(fd);
    const max = 2 * 1024 * 1024;
    if (
      !info.isFile() ||
      info.size > max ||
      (process.platform !== 'win32' && (info.mode & 0o077) !== 0)
    )
      throw new Error();
    const bytes = Buffer.alloc(max + 1);
    let count = 0;
    while (count < bytes.length) {
      const n = readSync(fd, bytes, count, bytes.length - count, null);
      if (!n) break;
      count += n;
    }
    if (count > max) throw new Error();
    return parseObject(
      new TextDecoder('utf-8', { fatal: true }).decode(
        bytes.subarray(0, count),
      ),
    ) as Seed;
  } catch {
    throw new Error('Invalid private API seed configuration');
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}
