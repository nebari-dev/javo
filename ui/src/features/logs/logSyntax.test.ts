import { describe, expect, it } from 'vitest'
import { highlightLine, type TokenKind } from './logSyntax'

// Sample lines use the formats the projects themselves write:
// klog: klog.go "Lmmdd hh:mm:ss.uuuuuu threadid file:line] msg".
// Jupyter Server: "[%(levelname)1.1s %(asctime)s.%(msecs).03d %(name)s] %(message)s" and log.py
//   "{status} {method} {uri} ({username}@{ip}) {request_time:.2f}ms".
// uvicorn: "%(levelprefix)s %(message)s" and '%(levelprefix)s %(client_addr)s - "%(request_line)s" %(status_code)s'.
// Streamlit: "%(asctime)s %(message)s" (default) and "%(asctime)s %(levelname) -7s %(name)s: %(message)s".
// RStudio Server: shared_core/Logger.cpp "<ISO 8601> [<program>] <LEVEL> <message>".
// Python logging BASIC_FORMAT "%(levelname)s:%(name)s:%(message)s"; Go slog TextHandler; zap JSON; nginx error log.

/** The kinded pieces of a line, as [text, kind]. */
function kinds(text: string): [string, TokenKind][] {
  return highlightLine(text).tokens.flatMap((t) => (t.kind ? [[t.text, t.kind] as [string, TokenKind]] : []))
}

const level = (text: string) => highlightLine(text).level

describe('highlightLine keeps the text', () => {
  it('joins back to the exact line', () => {
    const line = '[W 2026-10-30 12:00:02.345 ServerApp] 404 GET /u/alice/lab/api/kernels/abc (alice@10.42.0.1) 1.52ms referer=None'
    expect(highlightLine(line).tokens.map((t) => t.text).join('')).toBe(line)
  })
})

describe('plain prose', () => {
  it('stays one unmarked piece', () => {
    expect(highlightLine('  You can now view your Streamlit app in your browser.').tokens).toEqual([
      { text: '  You can now view your Streamlit app in your browser.' },
    ])
  })

  it('reads a quoted URL before a colon as a string, not a JSON key, and an IP and port as one number', () => {
    const k = kinds('Readiness probe failed: Get "http://10.42.0.17:8501/u/alice/sales-app/": dial tcp 10.42.0.17:8501: connect: connection refused')
    expect(k).toContainEqual(['"http://10.42.0.17:8501/u/alice/sales-app/"', 'string'])
    expect(k).toContainEqual(['10.42.0.17:8501', 'number'])
    expect(k.some(([, kind]) => kind === 'key')).toBe(false)
  })
})

describe('level detection', () => {
  it.each([
    ['E1031 12:00:01.123456       1 controller.go:316] "Reconciler error" err="pods \\"alice-sales-app\\" not found"', 'error'],
    ['W1031 12:00:01.123456       1 reflector.go:561] failed to list *v1.Pod: the server could not find the requested resource', 'warn'],
    ['I1031 12:00:00.000000       1 leaderelection.go:250] attempting to acquire leader lease javo-system/javo...', 'info'],
    ['[I 2026-10-30 12:00:01.234 ServerApp] Jupyter Server 2.14.2 is running at:', 'info'],
    ['[W 2026-10-30 12:00:02.345 ServerApp] 404 GET /u/alice/lab/api/kernels/abc (alice@10.42.0.1) 1.52ms referer=None', 'warn'],
    ['[E 2026-10-30 12:00:03.456 ServerApp] 500 GET /u/alice/lab/api/contents (alice@10.42.0.1) 12.10ms referer=None', 'error'],
    ['[D 2026-10-30 12:00:04.567 ServerApp] Searching [\'/home/alice\'] for config files', 'debug'],
    ['INFO:     Uvicorn running on http://0.0.0.0:8000 (Press CTRL+C to quit)', 'info'],
    ['WARNING:  Invalid HTTP request received.', 'warn'],
    ['ERROR:    Exception in ASGI application', 'error'],
    ['ERROR:root:could not reach the database', 'error'],
    ['CRITICAL:root:out of disk space', 'error'],
    ['DEBUG:urllib3.connectionpool:Starting new HTTPS connection (1): example.org:443', 'debug'],
    ["2026-10-30 12:00:01.234 WARNING streamlit.runtime.scriptrunner_utils.script_run_context: Thread 'MainThread': missing ScriptRunContext! This warning can be ignored when running in bare mode.", 'warn'],
    ["2026-10-30T12:00:01.123456Z [rsession-alice] ERROR system error 2 (No such file or directory) [path: /home/alice/.config/rstudio/rstudio-prefs.json]", 'error'],
    ['2026-10-30T12:00:00.500000Z [rserver] INFO Reading rserver configuration from \'/etc/rstudio/rserver.conf\'', 'info'],
    ['time=2026-10-30T12:00:01.000Z level=ERROR msg="request failed" status=502', 'error'],
    ['time=2026-10-30T12:00:01.000Z level=WARN msg="disk almost full"', 'warn'],
    ['{"level":"error","ts":1730289601.123,"msg":"reconcile failed","workspace":"sales-app"}', 'error'],
    ['{"level":"debug","ts":1730289601.123,"msg":"cache hit"}', 'debug'],
    ['2026/10/30 12:00:01 [error] 7#7: *1 connect() failed (111: Connection refused) while connecting to upstream', 'error'],
    ['Traceback (most recent call last):', 'error'],
    ["ModuleNotFoundError: No module named 'plotly'", 'error'],
    ['panic: runtime error: invalid memory address or nil pointer dereference', 'error'],
    ['fatal error: all goroutines are asleep - deadlock!', 'error'],
    ['Exception in thread "main" java.lang.IllegalStateException: boom', 'error'],
    ['TRACE sending ping', 'debug'],
  ])('%s is %s', (line, want) => {
    expect(level(line)).toBe(want)
  })

  it('takes the first level on the line, so an info line that mentions an error stays info', () => {
    expect(level('INFO  retrying after error: connection reset')).toBe('info')
  })

  it('marks a deprecation notice written as a sentence as a warning', () => {
    // Streamlit's own deprecation notice, in its default format (no level word).
    expect(level('`use_container_width` will be removed after 2025-12-31.')).toBe('warn')
    expect(level('2026-11-03 14:16:05 The `foo` argument is deprecated; use `bar`.')).toBe('warn')
    expect(level('Support for Python 3.9 is no longer supported in this release.')).toBe('warn')
  })

  it('leaves lines with no level unmarked', () => {
    expect(level('  You can now view your Streamlit app in your browser.')).toBeUndefined()
    expect(level('2026-10-30 12:00:01.234 Uncaught app execution')).toBeUndefined()
  })
})

describe('tokens', () => {
  it('klog: level letter, date and time, source file and line, quoted message, keys', () => {
    const k = kinds('E1031 12:00:01.123456       1 controller.go:316] "Reconciler error" err="not found" controller="workspace"')
    expect(k).toContainEqual(['E', 'error'])
    expect(k).toContainEqual(['1031 12:00:01.123456', 'time'])
    expect(k).toContainEqual(['controller.go:316', 'path'])
    expect(k).toContainEqual(['"Reconciler error"', 'string'])
    expect(k).toContainEqual(['err', 'key'])
    expect(k).toContainEqual(['controller', 'key'])
  })

  it('Jupyter access line: level, timestamp, component, status class, method, path, duration', () => {
    const k = kinds('[W 2026-10-30 12:00:02.345 ServerApp] 404 GET /u/alice/lab/api/kernels/abc (alice@10.42.0.1) 1.52ms referer=None')
    expect(k).toContainEqual(['W', 'warn'])
    expect(k).toContainEqual(['2026-10-30 12:00:02.345', 'time'])
    expect(k).toContainEqual(['ServerApp', 'source'])
    expect(k).toContainEqual(['404', 's4xx'])
    expect(k).toContainEqual(['GET', 'method'])
    expect(k).toContainEqual(['/u/alice/lab/api/kernels/abc', 'path'])
    expect(k).toContainEqual(['1.52ms', 'number'])
    expect(k).toContainEqual(['referer', 'key'])
  })

  it('uvicorn: URL and access line status classes', () => {
    expect(kinds('INFO:     Uvicorn running on http://0.0.0.0:8000 (Press CTRL+C to quit)')).toContainEqual(['http://0.0.0.0:8000', 'url'])
    const ok = kinds('INFO:     10.42.0.1:51234 - "GET /u/alice/api/ HTTP/1.1" 200 OK')
    expect(ok).toContainEqual(['GET', 'method'])
    expect(ok).toContainEqual(['200', 's2xx'])
    expect(kinds('INFO:     10.42.0.1:51234 - "POST /login HTTP/1.1" 302 Found')).toContainEqual(['302', 's3xx'])
    expect(kinds('INFO:     10.42.0.1:51234 - "GET /api/data HTTP/1.1" 500 Internal Server Error')).toContainEqual(['500', 's5xx'])
  })

  it('logfmt and JSON: keys, strings, numbers and the status code', () => {
    const slog = kinds('time=2026-10-30T12:00:01.000Z level=ERROR msg="request failed" status=502')
    expect(slog).toContainEqual(['time', 'key'])
    expect(slog).toContainEqual(['2026-10-30T12:00:01.000Z', 'time'])
    expect(slog).toContainEqual(['ERROR', 'error'])
    expect(slog).toContainEqual(['"request failed"', 'string'])
    expect(slog).toContainEqual(['502', 's5xx'])
    const zap = kinds('{"level":"error","ts":1730289601.123,"msg":"reconcile failed"}')
    expect(zap).toContainEqual(['"ts"', 'key'])
    expect(zap).toContainEqual(['"msg"', 'key'])
    expect(zap).toContainEqual(['1730289601.123', 'number'])
    expect(zap).toContainEqual(['"reconcile failed"', 'string'])
  })

  it('RStudio: ISO time, program, file path, C++ names and file:line', () => {
    const k = kinds(
      '2026-10-30T12:00:01.123456Z [rsession-alice] ERROR system error 2 (No such file or directory) [path: /home/alice/.config/rstudio/rstudio-prefs.json]; OCCURRED AT rstudio::core::Error rstudio::core::FilePath::openForRead() const src/cpp/shared_core/FilePath.cpp:2155',
    )
    expect(k).toContainEqual(['2026-10-30T12:00:01.123456Z', 'time'])
    expect(k).toContainEqual(['rsession-alice', 'source'])
    expect(k).toContainEqual(['ERROR', 'error'])
    expect(k).toContainEqual(['/home/alice/.config/rstudio/rstudio-prefs.json', 'path'])
    expect(k).toContainEqual(['rstudio::core::FilePath::openForRead', 'name'])
    expect(k).toContainEqual(['src/cpp/shared_core/FilePath.cpp:2155', 'path'])
  })

  it('syslog and nginx timestamps are times', () => {
    expect(kinds('Oct 30 12:00:01 node-1 kubelet[812]: Started container app')).toContainEqual(['Oct 30 12:00:01', 'time'])
    expect(kinds('2026/10/30 12:00:01 [error] 7#7: *1 connect() failed')).toContainEqual(['2026/10/30 12:00:01', 'time'])
  })

  // Python logging's default asctime ("%Y-%m-%d %H:%M:%S,mmm"), as Bokeh and Tornado print it.
  it('Python logging time with comma milliseconds is one time, before an unlevelled message', () => {
    const lines = [
      '2026-10-31 10:07:18,494 Starting Bokeh server version 3.9.2 (running on Tornado 6.6)',
      '2026-10-31 10:07:18,497 User authentication hooks NOT provided (default user enabled)',
      '2026-10-31 10:07:18,500 Bokeh app running at: http://localhost:8080/u/alice/sales-app/app',
      '2026-10-31 10:07:25,123 200 GET /u/alice/sales-app/app (10.244.0.5) 120.45ms',
    ]
    for (const line of lines) {
      expect(highlightLine(line).tokens[0]).toEqual({ text: line.slice(0, 23), kind: 'time' })
    }
    expect(kinds(lines[0])).toContainEqual(['3.9.2', 'number'])
    expect(kinds(lines[2])).toContainEqual(['http://localhost:8080/u/alice/sales-app/app', 'url'])
  })

  it('Python traceback: frames are frames, with the file and line still marked', () => {
    const k = kinds('  File "/home/alice/sales/app.py", line 3, in <module>')
    expect(k).toContainEqual(['  File ', 'frame'])
    expect(k).toContainEqual(['"/home/alice/sales/app.py"', 'path'])
    expect(k).toContainEqual(['3', 'number'])
    expect(kinds("ModuleNotFoundError: No module named 'plotly'")).toContainEqual(['ModuleNotFoundError', 'exception'])
    expect(kinds("KeyError: 'region'")).toContainEqual(['KeyError', 'exception'])
  })

  it('Go panic: goroutine header, function and file frames', () => {
    expect(kinds('goroutine 1 [running]:')).toContainEqual(['goroutine 1 [running]:', 'frame'])
    expect(kinds('main.main()')).toContainEqual(['main.main()', 'frame'])
    expect(kinds('net/http.(*conn).serve(0xc0001b2000, {0x7a1c28, 0xc000134000})')).toContainEqual([
      'net/http.(*conn).serve(0xc0001b2000, {0x7a1c28, 0xc000134000})',
      'frame',
    ])
    const file = kinds('\t/app/main.go:12 +0x1d')
    expect(file).toContainEqual(['/app/main.go:12', 'path'])
    expect(file).toContainEqual([' +0x1d', 'frame'])
  })

  it('Java: exception name and at frames', () => {
    expect(kinds('Exception in thread "main" java.lang.IllegalStateException: boom')).toContainEqual(['java.lang.IllegalStateException', 'exception'])
    expect(kinds('\tat com.example.App.main(App.java:12)').some(([, kind]) => kind === 'frame')).toBe(true)
  })
})
