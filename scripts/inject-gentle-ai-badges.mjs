import fs from 'node:fs';
import path from 'node:path';

/**
 * scripts/inject-gentle-ai-badges.mjs
 *
 * Injects the official "Built with Gentle-AI" badge into:
 *   1. docs/architecture/index.html:
 *      - In hero header under subtitle.
 *      - In footer centered above footer links.
 *   2. docs/architecture/gentle-mesh-test-verification-gates.html:
 *      - In .toolbar: .toolbar-brand-badge
 *      - In #step-explainer-modal footer: badge link next to close button.
 *      - In #test-tour-player header: small badge link next to tour step indicator.
 *   3. docs/architecture/gentle-mesh-secure-environment.html:
 *      - In .toolbar: .toolbar-brand-badge
 *      - In #step-explainer-modal footer: badge link next to close button.
 *      - In #test-tour-player header: small badge link next to tour step indicator.
 *
 * Specifications:
 *   - Image: https://raw.githubusercontent.com/Gentleman-Programming/gentle-ai/main/docs/assets/brand/built-with-gentle-ai.png
 *   - Link: https://github.com/Gentleman-Programming/gentle-ai
 *   - Completely idempotent.
 *   - Non-destructive to CSS layout / scrollHeight.
 */

const BADGE_IMG_URL = 'https://raw.githubusercontent.com/Gentleman-Programming/gentle-ai/main/docs/assets/brand/built-with-gentle-ai.png';
const BADGE_LINK_URL = 'https://github.com/Gentleman-Programming/gentle-ai';

const DRY_RUN = process.argv.includes('--dry-run') || process.argv.includes('--test');

function processIndexHtml() {
  const filePath = path.resolve('docs/architecture/index.html');
  if (!fs.existsSync(filePath)) {
    console.error(`File not found: ${filePath}`);
    return false;
  }
  let html = fs.readFileSync(filePath, 'utf8');
  let modified = false;

  // 1. Hero header badge under subtitle
  if (!html.includes('class="hero-brand-badge"')) {
    const subtitleMatch = html.match(/(<p class="subtitle"[^>]*>[\s\S]*?<\/p>)/);
    if (subtitleMatch) {
      const heroBadgeHtml = `      <!-- Built with Gentle-AI Hero Badge -->
      <div class="hero-brand-badge">
        <a href="${BADGE_LINK_URL}" target="_blank" rel="noopener noreferrer" title="Built with Gentle-AI">
          <img src="${BADGE_IMG_URL}" alt="Built with Gentle-AI">
        </a>
      </div>`;
      html = html.replace(subtitleMatch[1], subtitleMatch[1] + '\n' + heroBadgeHtml);
      modified = true;
    } else {
      console.warn('Could not locate subtitle in index.html');
    }
  }

  // 2. Footer badge centered above footer links
  if (!html.includes('class="footer-brand-badge"')) {
    const footerLinksIndex = html.indexOf('<div class="footer-links">');
    if (footerLinksIndex !== -1) {
      const footerBadgeHtml = `      <!-- Built with Gentle-AI Footer Badge -->
      <div class="footer-brand-badge">
        <a href="${BADGE_LINK_URL}" target="_blank" rel="noopener noreferrer" title="Built with Gentle-AI">
          <img src="${BADGE_IMG_URL}" alt="Built with Gentle-AI">
        </a>
      </div>\n`;
      html = html.slice(0, footerLinksIndex) + footerBadgeHtml + html.slice(footerLinksIndex);
      modified = true;
    } else {
      console.warn('Could not locate .footer-links in index.html');
    }
  }

  // 3. CSS styles
  if (!html.includes('.hero-brand-badge')) {
    const css = `
    /* Built with Gentle-AI Badges */
    .hero-brand-badge {
      display: flex;
      justify-content: center;
      align-items: center;
      margin: -0.5rem auto 1.25rem;
    }
    .hero-brand-badge a,
    .footer-brand-badge a {
      display: inline-flex;
      align-items: center;
      transition: transform 0.18s ease, opacity 0.18s ease;
    }
    .hero-brand-badge a:hover,
    .footer-brand-badge a:hover {
      transform: translateY(-1px);
      opacity: 0.9;
    }
    .hero-brand-badge img {
      height: 32px;
      width: auto;
      display: block;
    }
    .footer-brand-badge {
      display: flex;
      justify-content: center;
      align-items: center;
      margin: 0.75rem auto;
    }
    .footer-brand-badge img {
      height: 26px;
      width: auto;
      display: block;
    }
`;
    const styleEnd = html.lastIndexOf('</style>');
    if (styleEnd !== -1) {
      html = html.slice(0, styleEnd) + css + html.slice(styleEnd);
      modified = true;
    }
  }

  if (modified) {
    if (!DRY_RUN) fs.writeFileSync(filePath, html, 'utf8');
    console.log(`✓ Updated ${filePath}`);
  } else {
    console.log(`- Already up to date: ${filePath}`);
  }
  return true;
}

function processDiagramHtml(relativeFilePath) {
  const filePath = path.resolve(relativeFilePath);
  if (!fs.existsSync(filePath)) {
    console.error(`File not found: ${filePath}`);
    return false;
  }
  let html = fs.readFileSync(filePath, 'utf8');
  let modified = false;

  // A. .toolbar brand badge
  if (!html.includes('class="toolbar-brand-badge"')) {
    const toolbarMatch = html.match(/(<div class="toolbar"[^>]*>)/);
    if (toolbarMatch) {
      const toolbarBadgeHtml = `
    <a href="${BADGE_LINK_URL}" target="_blank" rel="noopener noreferrer" class="toolbar-brand-badge" title="Built with Gentle-AI">
      <img src="${BADGE_IMG_URL}" alt="Built with Gentle-AI">
    </a>`;
      html = html.replace(toolbarMatch[1], toolbarMatch[1] + toolbarBadgeHtml);
      modified = true;
    } else {
      console.warn(`Could not locate .toolbar in ${relativeFilePath}`);
    }
  }

  // B. #step-explainer-modal footer badge next to close button
  if (!html.includes('class="step-modal-brand-badge"')) {
    const nextBtnMatch = html.match(/(<button[^>]*id="step-btn-next"[^>]*>[\s\S]*?<\/button>)/);
    if (nextBtnMatch) {
      const footerBrandHtml = `
        <div class="step-modal-footer-brand">
          <a href="${BADGE_LINK_URL}" target="_blank" rel="noopener noreferrer" class="step-modal-brand-badge" title="Built with Gentle-AI">
            <img src="${BADGE_IMG_URL}" alt="Built with Gentle-AI">
          </a>
          <button type="button" class="step-nav-btn step-modal-btn-close" id="step-btn-close-modal" aria-label="Cerrar modal de explicación" onclick="document.getElementById('step-modal-close').click()">✕ Cerrar</button>
        </div>`;
      html = html.replace(nextBtnMatch[1], nextBtnMatch[1] + footerBrandHtml);
      modified = true;
    } else {
      console.warn(`Could not locate #step-btn-next in ${relativeFilePath}`);
    }
  }

  // C. #test-tour-player header small badge link next to tour step indicator
  if (!html.includes('class="tour-header-brand-badge"')) {
    const stepBadgeMatch = html.match(/(<span[^>]*id="tour-step-badge"[^>]*>[\s\S]*?<\/span>)/);
    if (stepBadgeMatch) {
      const tourBadgeHtml = `
          <a href="${BADGE_LINK_URL}" target="_blank" rel="noopener noreferrer" class="tour-header-brand-badge" title="Built with Gentle-AI">
            <img src="${BADGE_IMG_URL}" alt="Built with Gentle-AI">
          </a>`;
      html = html.replace(stepBadgeMatch[1], stepBadgeMatch[1] + tourBadgeHtml);
      modified = true;
    } else {
      console.warn(`Could not locate #tour-step-badge in ${relativeFilePath}`);
    }
  }

  // D. CSS injection
  if (!html.includes('.toolbar-brand-badge')) {
    const css = `
    /* ==========================================================
       BUILT WITH GENTLE-AI BRAND BADGES
       ========================================================== */
    .toolbar-brand-badge {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      height: 2.75rem;
      padding: 0.35rem 0.65rem;
      background: var(--toolbar-bg);
      border: 1px solid var(--toolbar-border);
      border-radius: 0.625rem;
      backdrop-filter: blur(10px);
      -webkit-backdrop-filter: blur(10px);
      box-shadow: 0 4px 14px rgba(0, 0, 0, 0.08);
      text-decoration: none;
      transition: background 0.15s ease, border-color 0.15s ease, transform 0.15s ease;
      flex-shrink: 0;
    }
    .toolbar-brand-badge:hover {
      background: var(--toolbar-hover, rgba(255, 255, 255, 0.1));
      border-color: #818cf8;
      transform: translateY(-1px);
    }
    .toolbar-brand-badge img {
      height: 20px;
      width: auto;
      display: block;
    }

    .step-modal-footer-brand {
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }
    .step-modal-brand-badge {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      padding: 0.25rem 0.45rem;
      border-radius: 6px;
      background: rgba(255, 255, 255, 0.05);
      border: 1px solid color-mix(in srgb, var(--toolbar-border) 60%, transparent);
      text-decoration: none;
      transition: all 0.15s ease;
      flex-shrink: 0;
    }
    .step-modal-brand-badge:hover {
      background: rgba(255, 255, 255, 0.12);
      border-color: #818cf8;
      transform: translateY(-1px);
    }
    .step-modal-brand-badge img {
      height: 18px;
      width: auto;
      display: block;
    }
    .step-modal-btn-close {
      color: var(--text-muted);
    }
    .step-modal-btn-close:hover {
      color: var(--text);
      border-color: #f43f5e !important;
    }

    .tour-header-brand-badge {
      display: inline-flex;
      align-items: center;
      opacity: 0.85;
      transition: opacity 0.15s ease, transform 0.15s ease;
      text-decoration: none;
      flex-shrink: 0;
      vertical-align: middle;
    }
    .tour-header-brand-badge:hover {
      opacity: 1;
      transform: translateY(-1px);
    }
    .tour-header-brand-badge img {
      height: 14px;
      width: auto;
      display: block;
    }

    @media (max-width: 600px) {
      .toolbar-brand-badge {
        height: 2.25rem;
        padding: 0.25rem 0.45rem;
      }
      .toolbar-brand-badge img {
        height: 16px;
      }
      .step-modal-footer-brand {
        order: 4;
        width: 100%;
        justify-content: space-between;
        margin-top: 0.25rem;
      }
    }

    html[data-present="true"]:not([data-embed="true"]) .toolbar-brand-badge {
      height: 2.2rem;
      padding: 0.25rem 0.5rem;
    }
    html[data-present="true"]:not([data-embed="true"]) .toolbar-brand-badge img {
      height: 16px;
    }

    @media print {
      .toolbar-brand-badge,
      .step-modal-brand-badge,
      .tour-header-brand-badge {
        display: none !important;
      }
    }
`;
    // Find the last </style> before </head>
    const headIndex = html.indexOf('</head>');
    const styleEnd = headIndex !== -1 ? html.lastIndexOf('</style>', headIndex) : html.lastIndexOf('</style>');
    if (styleEnd !== -1) {
      html = html.slice(0, styleEnd) + css + '\n  ' + html.slice(styleEnd);
      modified = true;
    }
  }

  if (modified) {
    if (!DRY_RUN) fs.writeFileSync(filePath, html, 'utf8');
    console.log(`✓ Updated ${filePath}`);
  } else {
    console.log(`- Already up to date: ${filePath}`);
  }
  return true;
}

function run() {
  console.log('=== Injecting "Built with Gentle-AI" Badges ===');
  processIndexHtml();
  processDiagramHtml('docs/architecture/gentle-mesh-test-verification-gates.html');
  processDiagramHtml('docs/architecture/gentle-mesh-secure-environment.html');
  console.log('=== Completed Successfully ===');
}

run();
