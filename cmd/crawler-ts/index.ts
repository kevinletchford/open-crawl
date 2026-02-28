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
const concurrency = parseInt(values.c as string, 10);

const queue: string[] = [targetURL];
const visited = new Set<string>([targetURL]);

let reqCount = 0;
let bytesRead = 0;
let activeWorkers = 0;

const linkRegex = /href=["'](.*?)["']/g;

async function worker() {
    activeWorkers++;
    
    while (queue.length > 0 && reqCount < maxReqs) {
        const url = queue.shift();
        if (!url) break;

        try {
            const res = await fetch(url, {
                // Keep-alive agent is default in modern node/bun fetch
            });
            const text = await res.text();
            
            bytesRead += text.length;
            reqCount++;
            
            if (reqCount >= maxReqs) break;

            let match;
            while ((match = linkRegex.exec(text)) !== null) {
                let link = match[1];
                if (link && link.startsWith('/')) {
                    link = "http://localhost:8080" + link;
                }
                
                if (link && !visited.has(link)) {
                    visited.add(link);
                    queue.push(link);
                }
            }
        } catch (e) {
            // Ignore errors for benchmarking raw throughput
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
