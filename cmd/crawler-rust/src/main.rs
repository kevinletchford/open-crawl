use clap::Parser;
use regex::Regex;
use serde::Serialize;
use std::collections::HashSet;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use tokio::sync::RwLock;
use async_channel::unbounded;
use std::collections::VecDeque;
use std::sync::Mutex;

#[derive(Parser, Debug)]
#[command(author, version, about, long_about = None)]
struct Args {
    #[arg(long, default_value = "http://localhost:8080/page/1")]
    url: String,

    #[arg(long, default_value_t = 10000)]
    max: usize,

    #[arg(short, default_value_t = 100)]
    c: usize,
}

#[derive(Serialize)]
struct BenchmarkResult {
    language: String,
    requests: usize,
    time_taken_ms: u128,
    req_per_sec: f64,
    bytes_read: usize,
}

#[tokio::main]
async fn main() {
    let args = Args::parse();
    let start = std::time::Instant::now();

    // The reqwest client with keep-alive
    let client = reqwest::Client::builder()
        .pool_max_idle_per_host(args.c)
        .build()
        .unwrap();

    let (tx, rx) = unbounded::<String>();
    
    // Send initial URL
    tx.send(args.url.clone()).await.unwrap();

    let visited = Arc::new(RwLock::new(HashSet::new()));
    visited.write().await.insert(args.url.clone());

    let req_count = Arc::new(AtomicUsize::new(0));
    let bytes_read = Arc::new(AtomicUsize::new(0));

    let recent_urls = Arc::new(Mutex::new(VecDeque::new()));

    let mut workers = vec![];
    
    // Naive link parsing for speed to match Go/TS behavior
    let link_regex = Regex::new(r#"href=["'](.*?)["']"#).unwrap();

    let base_url: String = args.url.split('/').take(3).collect::<Vec<_>>().join("/");

    let active_workers = Arc::new(AtomicUsize::new(0));

    for _ in 0..args.c {
        let client_clone = client.clone();
        let tx_clone = tx.clone();
        let rx_clone = rx.clone();
        let visited_clone = visited.clone();
        let req_count_clone = req_count.clone();
        let bytes_read_clone = bytes_read.clone();
        let active_clone = active_workers.clone();
        let link_regex_clone = link_regex.clone();
        let base_url_clone = base_url.clone();
        let recent_urls_clone = recent_urls.clone();
        let max_reqs = args.max;

        let worker = tokio::spawn(async move {
            while let Ok(url) = rx_clone.recv().await {
                // Check if we reached the max
                if req_count_clone.load(Ordering::Relaxed) >= max_reqs {
                    break;
                }

                {
                    let mut q = recent_urls_clone.lock().unwrap();
                    q.push_back(url.clone());
                    if q.len() > 20 {
                        q.pop_front();
                    }
                }

                active_clone.fetch_add(1, Ordering::Relaxed);

                match client_clone.get(&url).send().await {
                    Ok(resp) => {
                        if let Ok(text) = resp.text().await {
                            bytes_read_clone.fetch_add(text.len(), Ordering::Relaxed);
                            
                            let current_reqs = req_count_clone.fetch_add(1, Ordering::Relaxed) + 1;
                            
                            if current_reqs >= max_reqs {
                                // We are done, clear queue to stop others
                                rx_clone.close();
                                active_clone.fetch_sub(1, Ordering::Relaxed);
                                break;
                            }

                            for cap in link_regex_clone.captures_iter(&text) {
                                if let Some(matched) = cap.get(1) {
                                    let mut link = matched.as_str().to_string();
                                    
                                    if link.starts_with('/') {
                                        link = format!("{}{}", base_url_clone, link);
                                    }
                                    
                                    if !link.starts_with(&base_url_clone) {
                                        continue;
                                    }

                                    let mut v = visited_clone.write().await;
                                    if !v.contains(&link) {
                                        v.insert(link.clone());
                                        let _ = tx_clone.send(link).await;
                                    }
                                }
                            }
                        }
                    },
                    Err(_) => {},
                }
                
                active_clone.fetch_sub(1, Ordering::Relaxed);
            }
        });
        workers.push(worker);
    }
    
    // Wait for target hits
    loop {
        let current = req_count.load(Ordering::Relaxed);
        let recent_json = {
            let q = recent_urls.lock().unwrap();
            let vec: Vec<String> = q.iter().cloned().collect();
            serde_json::to_string(&vec).unwrap_or_else(|_| "[]".to_string())
        };
        eprintln!("PROGRESS: {} | {}", current, recent_json);
        if current >= args.max {
            rx.close();
            break;
        }
        
        let active = active_workers.load(Ordering::Relaxed);
        if rx.is_empty() && active == 0 {
            tokio::time::sleep(tokio::time::Duration::from_millis(50)).await;
            if rx.is_empty() && active_workers.load(Ordering::Relaxed) == 0 {
                rx.close();
                break;
            }
        }
        
        tokio::time::sleep(tokio::time::Duration::from_millis(100)).await;
    }

    // Wait for remaining
    for w in workers {
        let _ = w.await;
    }

    let duration_ms = start.elapsed().as_millis();
    let actual_reqs = req_count.load(Ordering::Relaxed);
    let req_per_sec = actual_reqs as f64 / (duration_ms as f64 / 1000.0);

    let res = BenchmarkResult {
        language: "Rust".to_string(),
        requests: actual_reqs,
        time_taken_ms: duration_ms,
        req_per_sec,
        bytes_read: bytes_read.load(Ordering::Relaxed),
    };

    println!("{}", serde_json::to_string(&res).unwrap());
    std::process::exit(0);
}
