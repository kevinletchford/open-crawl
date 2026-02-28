import { parseArgs } from 'util';

interface BenchmarkResult {
    language: string;
    requests: number;
    time_taken_ms: number;
    req_per_sec: number;
    bytes_read: number;
}

const { values } = parseArgs({
    args: process.argv.slice(2),
    options: {
        url: { type: 'string', default: 'http://localhost:8080/page/1' },
        max: { type: 'string', default: '10000' },
        c: { type: 'string', default: '100' },
    },
});

const targetURL = values.url as string;
const maxReqs = parseInt(values.max as string, 10);
const rawC = values.c as string;
const concurrency = parseInt(rawC.startsWith('=') ? rawC.slice(1) : rawC, 10);

const queue: string[] = [targetURL];
const visited = new Set<string>([targetURL]);

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
            await new Promise<void>((resolve, reject) => {
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
                                    link = "http://localhost:8080" + link;
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
        } catch (e: any) {
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
        console.error(`PROGRESS: ${reqCount}`);
        if (reqCount >= maxReqs || (queue.length === 0 && activeWorkers === 0)) {
            clearInterval(checkInterval);
            const duration = performance.now() - start;
            
            const result: BenchmarkResult = {
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
