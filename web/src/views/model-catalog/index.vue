<template>
  <div class="model-catalog">
    <div class="header">
      <h2>统一模型目录</h2>
      <el-button type="primary" @click="loadModels" :loading="loading">
        刷新
      </el-button>
    </div>

    <el-card>
      <el-table :data="models" stripe border>
        <el-table-column prop="id" label="模型 ID" width="200" />
        <el-table-column prop="display_name" label="显示名称" width="180" />
        <el-table-column prop="groups" label="可用分组" min-width="300">
          <template #default="{ row }">
            <el-tag
              v-for="group in row.groups"
              :key="group"
              size="small"
              style="margin-right: 5px"
            >
              {{ group }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150">
          <template #default="{ row }">
            <el-button size="small" type="primary" @click="viewDetails(row)">
              详情
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="summary">
        共 {{ models.length }} 个模型
      </div>
    </el-card>

    <el-dialog v-model="showDetails" title="模型详情" width="500px">
      <el-descriptions v-if="selectedModel" :column="1" border>
        <el-descriptions-item label="模型 ID">
          {{ selectedModel.id }}
        </el-descriptions-item>
        <el-descriptions-item label="显示名称">
          {{ selectedModel.display_name }}
        </el-descriptions-item>
        <el-descriptions-item label="可用分组">
          <el-tag
            v-for="group in selectedModel.groups"
            :key="group"
            size="small"
            style="margin-right: 5px"
          >
            {{ group }}
          </el-tag>
        </el-descriptions-item>
      </el-descriptions>
      <template #footer>
        <el-button @click="showDetails = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue';
import { ElMessage } from 'element-plus';
import { modelCatalogApi, type CatalogModel } from '../api/model-catalog';

const models = ref<CatalogModel[]>([]);
const loading = ref(false);
const showDetails = ref(false);
const selectedModel = ref<CatalogModel | null>(null);

async function loadModels() {
  loading.value = true;
  try {
    const resp = await modelCatalogApi.getModels();
    if (resp.object === 'list') {
      models.value = resp.data;
    } else {
      ElMessage.error('获取模型列表失败');
    }
  } catch (e) {
    ElMessage.error('获取模型列表失败');
  } finally {
    loading.value = false;
  }
}

function viewDetails(model: CatalogModel) {
  selectedModel.value = model;
  showDetails.value = true;
}

onMounted(() => {
  loadModels();
});
</script>

<style scoped>
.model-catalog {
  padding: 20px;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.header h2 {
  margin: 0;
}

.summary {
  margin-top: 20px;
  text-align: right;
  color: #666;
}
</style>
