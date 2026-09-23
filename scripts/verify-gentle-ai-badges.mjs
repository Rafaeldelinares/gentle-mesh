import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { ChromeVisualBrowser } from '/home/rafael/.agents/skills/archify/bin/visual-check.mjs';

const BADGE_IMG_URL = 'https://raw.githubusercontent.com/Gentleman-Programming/gentle-ai/main/docs/assets/brand/built-with-gentle-ai.png';
const BADGE_LINK_URL = 'https://github.com/Gentleman-Programming/gentle-ai';
const CHROME_PATH = '/usr/bin/chromium';

async function send(browser, sessionId, method, params = {}) {
  return browser.cdp.send(method, params, sessionId);
}

async function evaluate(browser, sessionId, expression) {
  const response = await send(browser, sessionId, 'Runtime.evaluate', {
    expression,
    returnByValue: true,
  });
  return response.result?.value;
}

async function main() {
  console.log('--- Verifying "Built with Gentle-AI" Badges in DOM & CDP ---');

  // Static checks
  const indexHtml = fs.readFileSync('docs/architecture/index.html', 'utf8');
  const gatesHtml = fs.readFileSync('docs/architecture/gentle-mesh-test-verification-gates.html', 'utf8');
  const secureHtml = fs.readFileSync('docs/architecture/gentle-mesh-secure-environment.html', 'utf8');

  // Check 1: index.html has hero and footer badges
  assert(indexHtml.includes('class="hero-brand-badge"'), 'hero-brand-badge not found in index.html');
  assert(indexHtml.includes('class="footer-brand-badge"'), 'footer-brand-badge not found in index.html');
  assert(indexHtml.includes(BADGE_LINK_URL), 'badge link url not found in index.html');
  console.log('✓ index.html static assertions passed');

  // Check 2: gates diagram
  assert(gatesHtml.includes('class="toolbar-brand-badge"'), 'toolbar-brand-badge not found in gates diagram');
  assert(gatesHtml.includes('class="step-modal-brand-badge"'), 'step-modal-brand-badge not found in gates diagram');
  assert(gatesHtml.includes('class="tour-header-brand-badge"'), 'tour-header-brand-badge not found in gates diagram');
  console.log('✓ gates diagram static assertions passed');

  // Check 3: secure diagram
  assert(secureHtml.includes('class="toolbar-brand-badge"'), 'toolbar-brand-badge not found in secure diagram');
  assert(secureHtml.includes('class="step-modal-brand-badge"'), 'step-modal-brand-badge not found in secure diagram');
  assert(secureHtml.includes('class="tour-header-brand-badge"'), 'tour-header-brand-badge not found in secure diagram');
  console.log('✓ secure diagram static assertions passed');

  // Live CDP verification
  const browser = new ChromeVisualBrowser(CHROME_PATH);
  try {
    const sessionId = await browser.sessionPromise;

    // Verify gates HTML
    const gatesUrl = 'file://' + path.resolve('docs/architecture/gentle-mesh-test-verification-gates.html');
    await send(browser, sessionId, 'Page.navigate', { url: gatesUrl });
    await new Promise((r) => setTimeout(r, 600));

    const gatesBadgeCheck = await evaluate(browser, sessionId, `(() => {
      const tb = document.querySelector('.toolbar-brand-badge');
      const modalBadge = document.querySelector('.step-modal-brand-badge');
      const tourBadge = document.querySelector('.tour-header-brand-badge');
      return {
        tbHref: tb ? tb.href : null,
        tbImg: tb ? tb.querySelector('img')?.src : null,
        modalBadgeHref: modalBadge ? modalBadge.href : null,
        tourBadgeHref: tourBadge ? tourBadge.href : null,
      };
    })()`);

    assert.equal(gatesBadgeCheck.tbHref, BADGE_LINK_URL);
    assert.equal(gatesBadgeCheck.tbImg, BADGE_IMG_URL);
    assert.equal(gatesBadgeCheck.modalBadgeHref, BADGE_LINK_URL);
    assert.equal(gatesBadgeCheck.tourBadgeHref, BADGE_LINK_URL);
    console.log('✓ Gates diagram CDP badge attributes verified in live DOM');

    // Verify index HTML
    const indexUrl = 'file://' + path.resolve('docs/architecture/index.html');
    await send(browser, sessionId, 'Page.navigate', { url: indexUrl });
    await new Promise((r) => setTimeout(r, 600));

    const indexBadgeCheck = await evaluate(browser, sessionId, `(() => {
      const hero = document.querySelector('.hero-brand-badge a');
      const footer = document.querySelector('.footer-brand-badge a');
      return {
        heroHref: hero ? hero.href : null,
        heroImg: hero ? hero.querySelector('img')?.src : null,
        footerHref: footer ? footer.href : null,
        footerImg: footer ? footer.querySelector('img')?.src : null,
      };
    })()`);

    assert.equal(indexBadgeCheck.heroHref, BADGE_LINK_URL);
    assert.equal(indexBadgeCheck.heroImg, BADGE_IMG_URL);
    assert.equal(indexBadgeCheck.footerHref, BADGE_LINK_URL);
    assert.equal(indexBadgeCheck.footerImg, BADGE_IMG_URL);
    console.log('✓ Index page CDP badge attributes verified in live DOM');
  } finally {
    await browser.close();
  }

  console.log('=== All Badge Injections & Visual Constraints Verified ===');
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
