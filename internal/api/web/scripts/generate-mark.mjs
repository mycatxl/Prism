import { chromium } from "@playwright/test";
const browser = await chromium.launch({
  headless: true,
  executablePath:
    "/home/ermit/.cache/ms-playwright/chromium-1234/chrome-linux64/chrome",
  args: ["--no-sandbox"],
});
const page = await browser.newPage({
  viewport: { width: 64, height: 64 },
  deviceScaleFactor: 2,
});
await page.setContent(
  `<body style="margin:0;background:transparent"><div style="width:64px;height:64px;background:#087f78;border-radius:14px;position:relative;overflow:hidden"><i style="position:absolute;inset:16px;border:3px solid #fff;border-radius:4px;transform:skewY(-12deg)"></i><b style="position:absolute;width:3px;height:39px;left:31px;top:11px;background:#5ec9b6;transform:rotate(12deg)"></b><em style="position:absolute;width:28px;height:3px;left:19px;top:31px;background:#d8a76c;transform:rotate(-12deg)"></em></div></body>`,
);
await page.screenshot({
  path: new URL("../public/prism-mark.png", import.meta.url).pathname,
  omitBackground: true,
});
await browser.close();
