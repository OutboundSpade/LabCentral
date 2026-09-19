// One route catalog owns titles, navigation, and global/workspace scope.
export const routeViews = {
  "": "labs",
  launch: "launch",
  templates: "templates",
  classroom: "classroom",
  teams: "teams",
  people: "people",
  settings: "settings",
  operations: "operations",
  lab: "detail",
  workspaces: "workspaces",
  plugins: "plugins",
  device: "device",
  account: "account",
  extension: "extension",
  feature: "feature",
} as const;
export type View = (typeof routeViews)[keyof typeof routeViews];
export const titles: Record<View, string> = {
  labs: "My labs",
  launch: "Launch a lab",
  templates: "Templates",
  classroom: "Classroom",
  teams: "Teams",
  people: "People",
  settings: "Settings",
  operations: "Operations",
  detail: "Lab details",
  workspaces: "Workspaces",
  plugins: "Integrations",
  device: "Authorize an application",
  account: "Your settings",
  extension: "Lab access",
  feature: "Extensions",
};
export const isGlobalView = (view: View) =>
  [
    "operations",
    "workspaces",
    "plugins",
    "device",
    "account",
    "extension",
  ].includes(view);
