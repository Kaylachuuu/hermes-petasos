// Render petasos-icon.svg to PNGs for make.py: node render.js petasos-icon.svg OUTDIR (needs Playwright).
const { chromium } = require('playwright');
const fs = require('fs');
(async () => {
  const svg = fs.readFileSync(process.argv[2], 'utf8');
  const b = await chromium.launch();
  for (const size of [16, 32, 64, 128, 256, 512, 1024]) {
    const p = await b.newPage({ viewport: { width: size, height: size } });
    await p.setContent(`<html><body style="margin:0;background:transparent">${svg.replace('width="1024" height="1024"', `width="${size}" height="${size}"`)}</body></html>`);
    await p.screenshot({ path: `${process.argv[3]}/icon_${size}.png`, omitBackground: true });
    await p.close();
  }
  await b.close();
})();
