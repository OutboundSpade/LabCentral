// One route catalog owns titles, navigation, and global/workspace scope.
export const routeViews = {
  "": "labs",
  launch: "launch",
  templates: "templates",
  classroom: "classroom",
  people: "people",
  settings: "settings",
  operations: "operations",
  lab: "detail",
  workspaces: "workspaces",
  plugins: "plugins",
  device: "device",
  account: "account",
  extension: "extension",
} as const;
export type View = (typeof routeViews)[keyof typeof routeViews];
export const titles: Record<View, string> = {
  labs: "My labs",
  launch: "Launch a lab",
  templates: "Templates",
  classroom: "Classroom",
  people: "People",
  settings: "Settings",
  operations: "Operations",
  detail: "Lab details",
  workspaces: "Workspaces",
  plugins: "Plugins",
  device: "Authorize gateway",
  account: "Your settings",
  extension: "Lab access",
};
export const isGlobalView = (view: View) =>
  ["operations", "workspaces", "plugins", "device", "account", "extension"].includes(view);
