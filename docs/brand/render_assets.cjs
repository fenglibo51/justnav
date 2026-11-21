#!/usr/bin/env node
/** Export JustNav V3 SVGs to PNG and a PNG-based multi-size favicon.ico.
 * Requires Node.js and `sharp` (`npm install sharp`).
 */
const fs = require('node:fs');
const path = require('node:path');
const sharp = require('sharp');
const dir = __dirname;
const p = (name) => path.join(dir, name);

async function raster(input, output, w, h) {
  await sharp(p(input), { density: 384 }).resize(w, h, { fit: 'fill' }).png().toFile(p(output));
}

function ico() {
  const sizes = [16, 32, 48];
  const images = sizes.map(n => fs.readFileSync(p(`favicon-${n}.png`)));
  const dirHeader = Buffer.alloc(6 + sizes.length * 16);
  dirHeader.writeUInt16LE(1, 2);
  dirHeader.writeUInt16LE(sizes.length, 4);
  let offset = dirHeader.length;
  sizes.forEach((n, i) => {
    const o = 6 + i * 16;
    dirHeader.writeUInt8(n, o);
    dirHeader.writeUInt8(n, o + 1);
    dirHeader.writeUInt16LE(1, o + 4);
    dirHeader.writeUInt16LE(32, o + 6);
    dirHeader.writeUInt32LE(images[i].length, o + 8);
    dirHeader.writeUInt32LE(offset, o + 12);
    offset += images[i].length;
  });
  fs.writeFileSync(p('favicon.ico'), Buffer.concat([dirHeader, ...images]));
}

async function main() {
  await raster('logo-mark.svg', 'logo-1024.png', 1024, 1024);
  await raster('logo-horizontal.svg', 'logo-horizontal-preview.png', 1300, 312);
  await raster('logo-horizontal-dark.svg', 'logo-horizontal-dark-preview.png', 1300, 312);
  await raster('logo-mark.svg', 'apple-touch-icon.png', 180, 180);
  for (const n of [16, 32, 48]) await raster('favicon.svg', `favicon-${n}.png`, n, n);
  ico();
  console.log('JustNav Portal V3 assets generated');
}
main().catch(err => { console.error(err); process.exitCode = 1; });
