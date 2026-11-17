// Dependency-free static server for the locally built demo client.
import { createServer } from 'node:http'
import { readFile, stat } from 'node:fs/promises'
import { resolve, extname, sep } from 'node:path'
const root = resolve('dist')
const types = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.json': 'application/json', '.ico': 'image/x-icon', '.txt': 'text/plain; charset=utf-8' }
createServer(async (req, res) => {
  if (!['GET', 'HEAD'].includes(req.method)) { res.writeHead(405); res.end(); return }
  try {
    let file = resolve(root, '.' + decodeURIComponent(new URL(req.url, 'http://localhost').pathname))
    if (file !== root && !file.startsWith(root + sep)) { res.writeHead(403); res.end(); return }
    try { if (!(await stat(file)).isFile()) file = resolve(root, 'index.html') }
    catch { file = resolve(root, 'index.html') }
    const bytes = await readFile(file)
    res.writeHead(200, { 'Content-Type': types[extname(file)] ?? 'application/octet-stream', 'Content-Length': bytes.length })
    res.end(req.method === 'HEAD' ? undefined : bytes)
  } catch { res.writeHead(400); res.end() }
}).listen(5173, '0.0.0.0')
