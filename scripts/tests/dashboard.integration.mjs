import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createHash, randomBytes } from 'node:crypto';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { chromium } from 'playwright';
import { databaseFixture } from '../testing/database.mjs';
import { dnsFixture } from '../testing/dns.mjs';
import { databaseClient } from '../../apps/api/src/database.ts';
import { root, freePort, start, stop, until } from '../testing/control.mjs';

test(
  'real dashboard browser: scoped management, encrypted sessions, DNS proof, pagination and outage recovery',
  { timeout: 180_000 },
  async (t) => {
    const fixture = await databaseFixture(),
      dns = await dnsFixture(),
      client = databaseClient(fixture.env.DATABASE_URL),
      processes = [];
    let browser;
    try {
      const apiPort = await freePort(),
        uiPort = await freePort();
      assert.notEqual(apiPort, uiPort);
      const origin = `http://127.0.0.1:${uiPort}`,
        apiBase = `http://127.0.0.1:${apiPort}/api/v1`;
      const env = {
        ...fixture.env,
        API_PORT: String(apiPort),
        API_DNS_SERVER: dns.server,
        DASHBOARD_API_URL: apiBase,
        DASHBOARD_ORIGIN: origin,
        DASHBOARD_SESSION_KEY_FILE: join(
          fixture.privateDir,
          'dashboard-session-key',
        ),
        DASHBOARD_API_TIMEOUT_MS: '1000',
        NEXT_TELEMETRY_DISABLED: '1',
        NODE_ENV: 'production',
      };
      let api = start(process.execPath, ['apps/api/src/index.ts'], env);
      processes.push(api);
      const dashboard = start(
        process.execPath,
        [
          'apps/dashboard/node_modules/next/dist/bin/next',
          'start',
          'apps/dashboard',
          '--hostname',
          '127.0.0.1',
          '--port',
          String(uiPort),
        ],
        env,
      );
      processes.push(dashboard);
      await until(async () => {
        try {
          return (
            (await fetch(apiBase.replace('/api/v1', '/health'))).ok &&
            (await fetch(origin + '/login')).ok
          );
        } catch {
          return false;
        }
      }, 30000);
      browser = await chromium.launch({
        ...(process.env.DASHBOARD_CHROMIUM_PATH
          ? { executablePath: process.env.DASHBOARD_CHROMIUM_PATH }
          : {}),
        args: ['--no-sandbox'],
        headless: true,
      });
      const context = await browser.newContext({
          viewport: { width: 1440, height: 1000 },
        }),
        page = await context.newPage();
      const consoleErrors = [];
      page.on('pageerror', (error) => consoleErrors.push(error.message));
      const responses = [];
      page.on('response', (response) => {
        const url = new URL(response.url());
        if (
          url.pathname.startsWith('/dashboard') ||
          url.pathname.startsWith('/api/control')
        ) {
          responses.push(
            response.request().method() +
              ' ' +
              url.pathname +
              ' ' +
              response.status(),
          );
        }
      });
      const login = async (target, token) => {
        target.setDefaultTimeout(10000);
        await target.goto(origin + '/login');
        await target.getByLabel('API key', { exact: true }).fill(token);
        await target
          .getByRole('button', { name: 'Sign in to workspace' })
          .click();
        await target.waitForURL(origin + '/dashboard');
        await target
          .getByRole('heading', { name: 'Overview', exact: true })
          .waitFor();
      };
      const cookies = async (current) =>
        (await current.cookies()).map((c) => c.name + '=' + c.value).join('; ');
      const proxy = async (
        path,
        method = 'GET',
        body,
        cookie,
        suppliedOrigin = origin,
      ) => {
        const response = await fetch(origin + path, {
          method,
          headers: {
            Cookie: cookie ?? (await cookies(context)),
            ...(method === 'GET'
              ? {}
              : { Origin: suppliedOrigin, 'Content-Type': 'application/json' }),
          },
          ...(body === undefined ? {} : { body: JSON.stringify(body) }),
          signal: AbortSignal.timeout(5000),
          redirect: 'manual',
        });
        return {
          response,
          value: response.status === 204 ? null : await response.json(),
        };
      };
      let tunnelId, projectId, domainId, cookie;
      let failed = false;
      const step = async (name, run) => {
        if (failed) {
          await t.test(
            name,
            { skip: 'An earlier workflow step failed' },
            () => {},
          );
          return;
        }
        await t.test(name, async (current) => {
          try {
            await run();
          } catch (error) {
            failed = true;
            current.diagnostic('Failed page: ' + new URL(page.url()).pathname);
            current.diagnostic('Browser errors: ' + consoleErrors.join(', '));
            current.diagnostic(
              'Page alerts: ' +
                (await page.getByRole('alert').allTextContents()).join(', '),
            );
            current.diagnostic(
              'Recent metadata replies: ' + responses.slice(-15).join(', '),
            );
            throw error;
          }
        });
      };
      await step(
        'login gates protected pages and gives only an encrypted HttpOnly session cookie',
        async () => {
          const response = await fetch(origin + '/dashboard/tunnels', {
            redirect: 'manual',
          });
          assert.equal(response.status, 307);
          assert.equal(response.headers.get('location'), '/login');
          await login(page, fixture.bearer);
          const jar = await context.cookies();
          assert.equal(jar.length, 1);
          assert.equal(jar[0].httpOnly, true);
          assert.equal(jar[0].sameSite, 'Lax');
          assert.ok(jar[0].value !== fixture.bearer);
          cookie = await cookies(context);
          const session = await client.apiSession.findFirst();
          assert.ok(session);
          assert.ok(!jar[0].value.includes(session.tokenHash));
          assert.equal(
            await page.evaluate(() => globalThis.document.cookie),
            '',
          );
          assert.deepEqual(
            await page.evaluate(() => [
              localStorage.length,
              sessionStorage.length,
            ]),
            [0, 0],
          );
          const html = await page.content();
          assert.ok(
            !html.includes(fixture.bearer) && !html.includes(jar[0].value),
          );
          const invalid = await proxy(
            '/api/control/me',
            'GET',
            undefined,
            'portway_session=' + fixture.bearer,
          );
          assert.equal(invalid.response.status, 401);
          const screenshots = join(root, '.tmp/screenshots');
          mkdirSync(screenshots, { recursive: true });
          await page.screenshot({
            path: join(screenshots, 'phase13-overview.png'),
            fullPage: true,
          });
          await page.setViewportSize({ width: 390, height: 844 });
          assert.equal(
            await page.evaluate(
              () =>
                globalThis.document.documentElement.scrollWidth <=
                globalThis.innerWidth,
            ),
            true,
          );
          await page.screenshot({
            path: join(screenshots, 'phase13-mobile.png'),
            fullPage: true,
          });
          await page.setViewportSize({ width: 1440, height: 1000 });
        },
      );
      await step(
        'project and tunnel creation, detail and fixed CLI instructions preserve generation',
        async () => {
          await page.goto(origin + '/dashboard/tunnels');
          await page
            .getByRole('button', { name: 'New project', exact: true })
            .click();
          let dialog = page.getByRole('dialog', { name: 'New project' });
          await dialog.getByLabel('Project name').fill('Browser project');
          await dialog.getByLabel('Project slug').fill('browser-project');
          await dialog.getByRole('button', { name: 'Create project' }).click();
          await dialog.waitFor({ state: 'hidden' });
          await page
            .getByRole('button', { name: 'New tunnel', exact: true })
            .click();
          dialog = page.getByRole('dialog', { name: 'New tunnel' });
          await dialog.getByLabel('Tunnel name').fill('Browser webhook');
          await dialog
            .getByLabel('Project', { exact: true })
            .selectOption({ label: 'Browser project' });
          await dialog.getByLabel('Local port').fill('4317');
          await dialog.getByRole('button', { name: 'Create tunnel' }).click();
          await dialog.waitFor({ state: 'hidden' });
          await page
            .getByRole('link', { name: 'Browser webhook', exact: true })
            .click();
          await page
            .getByRole('heading', { name: 'Browser webhook', exact: true })
            .waitFor();
          const tunnel = await client.tunnel.findFirst({
            where: { name: 'Browser webhook' },
          });
          tunnelId = tunnel.id;
          projectId = tunnel.projectId;
          assert.equal(tunnel.generation.toFixed(0), '0');
          assert.equal(tunnel.localPort, 4317);
          assert.ok(
            (await page.locator('.command-box').textContent()).includes(
              `PORTWAY_TUNNEL_ID=${tunnelId} portway 4317`,
            ),
          );
          assert.equal(
            await client.tunnelCredential.count({ where: { tunnelId } }),
            0,
          );
        },
      );
      await step(
        'domain UI issues a one-time TXT proof, verifies real DNS, activates and disables',
        async () => {
          await page.goto(origin + '/dashboard/domains');
          await page
            .getByRole('button', { name: 'Add domain', exact: true })
            .click();
          const dialog = page.getByRole('dialog', { name: 'Add domain' });
          await dialog.getByLabel('Hostname').fill('browser.example.test');
          await dialog
            .getByLabel('Tunnel', { exact: true })
            .selectOption({ label: 'Browser webhook' });
          await dialog
            .getByRole('button', { name: 'Generate ownership record' })
            .click();
          await dialog
            .getByText('Publish this DNS TXT record', { exact: true })
            .waitFor();
          const name = await dialog
              .locator('.copy-field code')
              .nth(0)
              .textContent(),
            proof = await dialog
              .locator('.copy-field code')
              .nth(1)
              .textContent();
          assert.match(proof, /^portway-verification=/);
          dns.records.set(name, [proof]);
          const domain = await client.domain.findUnique({
            where: { hostname: 'browser.example.test' },
          });
          domainId = domain.id;
          assert.ok(!JSON.stringify(domain).includes(proof));
          await dialog
            .getByRole('button', { name: 'I saved the TXT record' })
            .click();
          const card = page.locator('.domain-card').filter({
            has: page.getByRole('heading', {
              name: 'browser.example.test',
              exact: true,
            }),
          });
          const verifiedReply = page.waitForResponse(
            (response) =>
              response.url() ===
              origin + '/api/control/domains/' + domainId + '/verify',
          );
          await card.getByRole('button', { name: 'Verify DNS' }).click();
          const verified = await verifiedReply;
          const verifiedData =
            verified.status() === 200 ? null : await verified.json();
          assert.equal(
            verified.status(),
            200,
            'DNS verification failed: ' +
              (verifiedData?.error?.code ?? 'unknown') +
              ', DNS queries=' +
              dns.queries,
          );
          assert.equal(
            (await client.domain.findUnique({ where: { id: domainId } }))
              .status,
            'VERIFIED',
          );
          await until(
            async () =>
              (await card.locator('.badge').textContent()).trim() ===
              'verified',
          ).catch(async () => {
            const latest = await proxy('/api/control/domains/' + domainId);
            throw new Error(
              'Displayed domain state=' +
                (await card.locator('.badge').textContent()).trim() +
                ', API=' +
                (latest.value?.data?.domain?.status ??
                  latest.value?.error?.code),
            );
          });
          const activatedReply = page.waitForResponse(
            (response) =>
              response.url() ===
              origin + '/api/control/domains/' + domainId + '/activate',
          );
          await card.getByRole('button', { name: 'Activate domain' }).click();
          const activated = await activatedReply;
          const activatedData =
            activated.status() === 200 ? null : await activated.json();
          assert.equal(
            activated.status(),
            200,
            'Activation failed: ' + (activatedData?.error?.code ?? 'unknown'),
          );
          assert.equal(
            (await client.domain.findUnique({ where: { id: domainId } }))
              .status,
            'ACTIVE',
          );
          await until(
            async () =>
              (await card.locator('.badge').textContent()).trim() === 'active',
          ).catch(async () => {
            throw new Error(
              'Displayed domain state after activation=' +
                (await card.locator('.badge').textContent()).trim(),
            );
          });
          await card
            .getByRole('button', { name: 'Disable', exact: true })
            .click();
          await card
            .getByLabel('Type hostname to disable')
            .fill('browser.example.test');
          await card.getByRole('button', { name: 'Confirm disable' }).click();
          await until(
            async () =>
              (await card.locator('.badge').textContent()).trim() ===
              'disabled',
          );
          assert.equal(
            (await client.domain.findUnique({ where: { id: domainId } }))
              .verificationHash,
            null,
          );
          assert.equal(await page.locator('.proof-panel').count(), 0);
          await card
            .getByRole('button', { name: 'Re-enable with new proof' })
            .click();
          const renewed = page.getByRole('dialog', {
            name: 'Domain ownership record',
          });
          await renewed
            .getByText('Publish this DNS TXT record', { exact: true })
            .waitFor();
          const newProof = await renewed
            .locator('.copy-field code')
            .nth(1)
            .textContent();
          assert.match(newProof, /^portway-verification=/);
          assert.notEqual(newProof, proof);
          await renewed
            .getByRole('button', { name: 'I saved the TXT record' })
            .click();
          await until(
            async () =>
              (await card.locator('.badge').textContent()).trim() ===
              'pending verification',
          );
          assert.equal(await page.locator('.proof-panel').count(), 0);
          assert.deepEqual(
            await page.evaluate(() => [
              localStorage.length,
              sessionStorage.length,
            ]),
            [0, 0],
          );
        },
      );
      await step(
        'relays and logs display authoritative status; settings exposes account scope',
        async () => {
          await page.goto(origin + '/dashboard/relays');
          await page
            .getByRole('heading', { name: 'Relays', exact: true })
            .waitFor();
          assert.equal(
            (await page.locator('.relay-card .badge').textContent()).trim(),
            'offline',
          );
          assert.equal(
            await page.getByRole('button', { name: 'Drain' }).count(),
            0,
          );
          await page.goto(origin + '/dashboard/logs?tunnelId=' + tunnelId);
          await page
            .getByRole('heading', {
              name: 'Traffic logs are coming in Phase 14',
            })
            .waitFor();
          await page.goto(origin + '/dashboard/settings');
          assert.ok(
            (await page.locator('.details').last().textContent()).includes(
              'owner',
            ),
          );
        },
      );
      await step(
        'CSRF, oversized input and internal routes fail before mutation',
        async () => {
          const count = await client.project.count();
          const cross = await proxy(
            '/api/control/projects',
            'POST',
            { name: 'Injected', slug: 'injected' },
            cookie,
            'https://attacker.test',
          );
          assert.equal(cross.response.status, 403);
          const missing = await fetch(origin + '/api/session', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ token: fixture.bearer }),
          });
          assert.equal(missing.status, 403);
          const large = await proxy(
            '/api/control/projects',
            'POST',
            { name: 'x'.repeat(65536), slug: 'large' },
            cookie,
          );
          assert.equal(large.response.status, 413);
          for (const path of [
            '/api/control/internal/relays/rel_local/drain',
            '/api/control/tunnels/' + tunnelId + '/connect',
          ])
            assert.equal((await proxy(path, 'POST', {})).response.status, 404);
          assert.equal(await client.project.count(), count);
        },
      );
      await step(
        'viewer controls stay read-only and cross-organization identifiers return not found',
        async () => {
          const tokens = [
              randomBytes(32).toString('base64url'),
              randomBytes(32).toString('base64url'),
            ],
            expiresAt = new Date(Date.now() + 3600000);
          await client.organization.create({
            data: {
              id: 'org_browser_foreign',
              name: 'Foreign workspace',
              slug: 'browser-foreign',
            },
          });
          for (let i = 0; i < 2; i++) {
            const id = 'usr_browser_' + i,
              organizationId = i === 0 ? 'org_local' : 'org_browser_foreign';
            await client.user.create({
              data: { id, email: `browser${i}@example.test` },
            });
            await client.membership.create({
              data: {
                id: 'mem_browser_' + i,
                userId: id,
                organizationId,
                role: i === 0 ? 'VIEWER' : 'OWNER',
              },
            });
            await client.apiKey.create({
              data: {
                id: 'key_browser_' + i,
                name: 'Browser fixture',
                userId: id,
                organizationId,
                tokenHash: createHash('sha256').update(tokens[i]).digest('hex'),
                expiresAt,
              },
            });
          }
          const viewerContext = await browser.newContext(),
            viewerPage = await viewerContext.newPage();
          await login(viewerPage, tokens[0]);
          await viewerPage.goto(origin + '/dashboard/tunnels');
          assert.equal(
            await viewerPage
              .getByRole('button', { name: 'New tunnel' })
              .count(),
            0,
          );
          const denied = await proxy(
            '/api/control/projects',
            'POST',
            { name: 'Viewer attempt', slug: 'viewer-attempt' },
            await cookies(viewerContext),
          );
          assert.equal(denied.response.status, 403);
          const foreignContext = await browser.newContext(),
            foreignPage = await foreignContext.newPage();
          await login(foreignPage, tokens[1]);
          assert.equal(
            (
              await proxy(
                '/api/control/tunnels/' + tunnelId,
                'GET',
                undefined,
                await cookies(foreignContext),
              )
            ).response.status,
            404,
          );
          assert.equal(
            (
              await proxy(
                '/api/control/domains/' + domainId,
                'GET',
                undefined,
                await cookies(foreignContext),
              )
            ).response.status,
            404,
          );
          await foreignPage.goto(origin + '/dashboard/tunnels/' + tunnelId);
          await foreignPage
            .getByRole('heading', { name: 'Tunnel unavailable' })
            .waitFor();
          assert.ok(!(await foreignPage.content()).includes('Browser webhook'));
          await viewerContext.close();
          await foreignContext.close();
        },
      );
      await step(
        'explicit revocation and signed cursor pagination work without issuing credentials',
        async () => {
          await page.goto(origin + '/dashboard/tunnels/' + tunnelId);
          await page
            .getByRole('button', { name: 'Revoke tunnel', exact: true })
            .click();
          const dialog = page.getByRole('dialog', { name: 'Revoke tunnel' });
          await dialog.getByLabel('Type tunnel name').fill('Browser webhook');
          await dialog
            .getByRole('button', { name: 'Confirm revocation' })
            .click();
          await dialog.waitFor({ state: 'hidden' });
          await until(
            async () =>
              (await client.tunnel.findUnique({ where: { id: tunnelId } }))
                .status === 'REVOKED',
          );
          await client.tunnel.createMany({
            data: Array.from({ length: 51 }, (_, i) => ({
              id: 'tnl_browser_page_' + String(i).padStart(3, '0'),
              projectId,
              name: 'Page tunnel ' + i,
              slug: 'page-' + i,
              type: 'PERSISTENT',
            })),
          });
          await page.goto(origin + '/dashboard/tunnels?projectId=' + projectId);
          assert.equal(await page.locator('tbody tr').count(), 50);
          await page.getByRole('link', { name: 'Next page' }).click();
          await until(
            async () => (await page.locator('tbody tr').count()) === 2,
          );
          assert.equal(await client.tunnelCredential.count(), 0);
        },
      );
      await step(
        'API restart retains browser auth; outages preserve cookie and local logout is explicit',
        async () => {
          const staleCursor = page.url();
          await stop(api);
          api = start(process.execPath, ['apps/api/src/index.ts'], env);
          processes.push(api);
          await until(async () => {
            try {
              return (await fetch(apiBase.replace('/api/v1', '/health'))).ok;
            } catch {
              return false;
            }
          });
          await page.goto(staleCursor);
          await page.getByRole('alert').first().waitFor();
          await page.getByRole('link', { name: 'First page' }).click();
          await until(
            async () => (await page.locator('tbody tr').count()) === 50,
          );
          await page.goto(origin + '/dashboard');
          await page
            .getByRole('heading', { name: 'Overview', exact: true })
            .waitFor();
          await stop(api);
          const startTime = Date.now(),
            outage = await proxy('/api/control/me');
          assert.equal(outage.response.status, 503);
          assert.ok(Date.now() - startTime < 2500);
          assert.equal(outage.response.headers.has('set-cookie'), false);
          await page.goto(origin + '/dashboard');
          await page.getByRole('alert').first().waitFor();
          await page
            .getByRole('button', { name: 'Sign out', exact: true })
            .click();
          await page.waitForURL(origin + '/login?reason=local-logout');
          await page
            .getByText('Signed out locally.', { exact: false })
            .waitFor();
          assert.equal((await context.cookies()).length, 0);
          api = start(process.execPath, ['apps/api/src/index.ts'], env);
          processes.push(api);
          await until(async () => {
            try {
              return (await fetch(apiBase.replace('/api/v1', '/health'))).ok;
            } catch {
              return false;
            }
          });
          await login(page, fixture.bearer);
          const liveCookie = await cookies(context);
          await page
            .getByRole('button', { name: 'Sign out', exact: true })
            .click();
          await page.waitForURL(origin + '/login');
          assert.equal(
            (await proxy('/api/control/me', 'GET', undefined, liveCookie))
              .response.status,
            401,
          );
        },
      );
      if (!failed) assert.deepEqual(consoleErrors, []);
      for (const process of processes)
        assert.ok(
          !process.output().includes(fixture.bearer) &&
            !process.output().includes(fixture.relayBearer),
        );
      if (!failed)
        t.diagnostic(
          'Browser flows use real PostgreSQL/API and DNS; encrypted cookies, tenant boundaries and outage behavior verified.',
        );
    } finally {
      await browser?.close();
      for (const process of processes.reverse()) await stop(process);
      await client.$disconnect();
      await dns.close();
      await fixture.close();
    }
  },
);
