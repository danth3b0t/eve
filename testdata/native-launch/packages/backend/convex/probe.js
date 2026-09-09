import { queryGeneric } from "convex/server";

// Public, non-secret acceptance markers only; never return the whole environment.
export const connection = queryGeneric({
  args: {},
  handler: () => ({
    siteUrl: process.env.SITE_URL ?? null,
    defaultMarker: process.env.EVE_M0_DEFAULT ?? null,
  }),
});
