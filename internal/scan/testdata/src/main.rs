use std::collections::HashMap;
use reqwest::{Client, header::{self, HeaderMap as Headers}};
use serde::*;
use crate::config::Config;
extern crate log as logging;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let client = Client::new();
    let mut h = Headers::new();
    h.insert(header::ACCEPT, "application/json".parse()?);
    let res = client.get("https://api.example.com/v1/items").headers(h).send().await?;
    let v: serde_json::Value = serde_json::from_str(&res.text().await?)?;
    let s = r#"reqwest::Client in a raw string"#;
    logging::info!("{}", 'x');
    fn borrow<'a>(x: &'a str) -> &'a str { x }
    let _m: HashMap<String, String> = HashMap::new();
    Ok(())
}
