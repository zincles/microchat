import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css" };

createServer(async (req, res) => {
  const path = req.url === "/" ? "/index.html" : req.url;
  try {
    const body = await readFile(join(root, path));
    const ext = path.slice(path.lastIndexOf("."));
    res.writeHead(200, { "Content-Type": types[ext] ?? "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end("not found");
  }
}).listen(5173, () => console.log("preview on :5173 (static only, no vite)"));
