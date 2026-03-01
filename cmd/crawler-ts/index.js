import { parseArgs } from 'util';
const { values } = parseArgs({
    args: process.argv.slice(2),
    options: {
        url: { type: 'string', default: 'http://localhost:8080/page/1' },
        max: { type: 'string', default: '10000' },
        c: { type: 'string', default: '100' },
    },
});
const targetURL = values.url;
const baseURL = new URL(targetURL).origin;
const maxReqs = parseInt(values.max, 10);
const rawC = values.c;
const concurrency = parseInt(rawC.startsWith('=') ? rawC.slice(1) : rawC, 10);
const queue = [targetURL];
const visited = new Set([targetURL]);
const recentURLs = [];
let reqCount = 0;
let bytesRead = 0;
let activeWorkers = 0;
const linkRegex = /href=["'](.*?)["']/g;
import * as http from 'http';
const agent = new http.Agent({
    keepAlive: true,
    maxSockets: concurrency * 2,
    timeout: 30000
});
async function worker() {
    activeWorkers++;
    while (reqCount < maxReqs) {
        const url = queue.shift();
        if (!url) {
            if (activeWorkers === 1) {
                break; // Last active worker, queue is empty, we're done
            }
            // Temporarily not active while waiting for queue to populate
            activeWorkers--;
            await new Promise(r => setTimeout(r, 1));
            activeWorkers++;
            continue;
        }
        try {
            recentURLs.push(url);
            if (recentURLs.length > 20) {
                recentURLs.shift();
            }
            await new Promise((resolve, reject) => {
                http.get(url, { agent }, (res) => {
                    let text = '';
                    res.on('data', (chunk) => {
                        text += chunk;
                        bytesRead += chunk.length;
                    });
                    res.on('end', () => {
                        reqCount++;
                        if (reqCount < maxReqs) {
                            const matches = text.matchAll(linkRegex);
                            for (const match of matches) {
                                let link = match[1];
                                if (link && link.startsWith('/')) {
                                    link = baseURL + link;
                                }
                                if (!link || !link.startsWith(baseURL)) {
                                    continue;
                                }
                                if (link && !visited.has(link)) {
                                    visited.add(link);
                                    queue.push(link);
                                }
                            }
                        }
                        resolve();
                    });
                }).on('error', (err) => {
                    resolve();
                });
            });
        }
        catch (e) {
            // Ignore
        }
    }
    activeWorkers--;
}
async function main() {
    const start = performance.now();
    // Start workers
    const workers = [];
    for (let i = 0; i < concurrency; i++) {
        workers.push(worker());
    }
    // Wait for all workers to finish or queue to empty
    const checkInterval = setInterval(() => {
        const recentData = JSON.stringify(recentURLs);
        console.error(`PROGRESS: ${reqCount} | ${recentData}`);
        if (reqCount >= maxReqs || (queue.length === 0 && activeWorkers === 0)) {
            clearInterval(checkInterval);
            const duration = performance.now() - start;
            const result = {
                language: "TypeScript",
                requests: reqCount,
                time_taken_ms: Math.round(duration),
                req_per_sec: reqCount / (duration / 1000),
                bytes_read: bytesRead
            };
            console.log(JSON.stringify(result));
            process.exit(0);
        }
    }, 10);
}
main();
//# sourceMappingURL=index.js.map