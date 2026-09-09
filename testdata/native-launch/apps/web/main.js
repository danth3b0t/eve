const config = {
  url: import.meta.env.VITE_CONVEX_URL,
  siteUrl: import.meta.env.VITE_CONVEX_SITE_URL,
};
document.querySelector("#config").textContent = JSON.stringify(config, null, 2);
