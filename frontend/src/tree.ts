import type { Entry } from "./api";

export type TreeNode = {
  name: string;
  path: string;
  file: Entry | null;
  children: TreeNode[];
};

export function buildTree(files: Entry[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", file: null, children: [] };

  for (const file of files) {
    const parts = file.path.split("/");
    let node = root;
    for (let i = 0; i < parts.length - 1; i += 1) {
      const segment = parts[i];
      let child = node.children.find((item) => item.file === null && item.name === segment);
      if (!child) {
        child = { name: segment, path: parts.slice(0, i + 1).join("/"), file: null, children: [] };
        node.children.push(child);
      }
      node = child;
    }
    node.children.push({
      name: parts[parts.length - 1],
      path: file.path,
      file,
      children: [],
    });
  }

  sort(root);
  return root.children;
}

function sort(node: TreeNode): void {
  node.children.sort((a, b) => {
    const aDir = a.file === null;
    const bDir = b.file === null;
    if (aDir !== bDir) return aDir ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  node.children.forEach(sort);
}
