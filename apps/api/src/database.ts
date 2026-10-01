import { PrismaClient, Prisma } from '@prisma/client';
import { AsyncLocalStorage } from 'node:async_hooks';
import { ApiFailure } from './validation.ts';

export type Transaction = Prisma.TransactionClient;
export function storageFailure() {
  return new ApiFailure(
    503,
    'STORAGE_UNAVAILABLE',
    'Control-plane storage unavailable',
  );
}
export function databaseClient(url: string) {
  let parsed: URL;
  try {
    parsed = new URL(url);
    if (
      !['postgresql:', 'postgres:'].includes(parsed.protocol) ||
      !parsed.hostname
    )
      throw new Error();
  } catch {
    throw new Error('Invalid database configuration');
  }
  parsed.searchParams.set('connection_limit', '8');
  parsed.searchParams.set('pool_timeout', '2');
  parsed.searchParams.set('connect_timeout', '3');
  parsed.searchParams.set('socket_timeout', '3');
  return new PrismaClient({
    datasourceUrl: parsed.toString(),
    errorFormat: 'minimal',
    log: [],
  });
}
export class Database {
  private readonly context = new AsyncLocalStorage<Transaction>();
  readonly client: PrismaClient;
  constructor(client: PrismaClient) {
    this.client = client;
  }
  async work<T>(
    write: boolean,
    operation: (tx: Transaction) => Promise<T>,
  ): Promise<T> {
    const current = this.context.getStore();
    if (current) return operation(current);
    try {
      return await this.client.$transaction(
        async (tx) => {
          await tx.$executeRaw`SET LOCAL statement_timeout = '1500ms'`;
          await tx.$executeRaw`SET LOCAL lock_timeout = '1000ms'`;
          if (write)
            await tx.$queryRaw`SELECT pg_advisory_xact_lock(1347375700, 10)::text`;
          return this.context.run(tx, () => operation(tx));
        },
        {
          maxWait: 1000,
          timeout: 3000,
          isolationLevel: Prisma.TransactionIsolationLevel.ReadCommitted,
        },
      );
    } catch (error) {
      if (error instanceof ApiFailure) throw error;
      if (
        error instanceof Prisma.PrismaClientKnownRequestError &&
        error.code === 'P2002'
      )
        throw new ApiFailure(
          409,
          'RESOURCE_CONFLICT',
          'Resource already exists',
        );
      throw storageFailure();
    }
  }
  close() {
    return this.client.$disconnect();
  }
}
